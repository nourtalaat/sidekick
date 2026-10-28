package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/slackapp"
	"github.com/nourtalaat/sidekick/internal/slackcmd"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// registerSlackApp receives Slack events when the slack section lets the
// app receive them: over Socket Mode with the app token, or at
// POST /slack/events in events mode, and answers /sidekick commands the
// same way, at POST /slack/commands in events mode. Without either,
// Sidekick only posts to Slack.
func registerSlackApp(ctx context.Context, a *app.App, cache *rules.Cache) error {
	cfg := a.Config()
	log := a.Logger().With("component", "slackapp")

	if !cfg.Slack.Connects() {
		if cfg.Slack != nil {
			log.InfoContext(ctx, "not receiving Slack events: slack.appTokenSecret is not set, so Sidekick only posts to Slack")
		}

		return nil
	}

	h, err := slackapp.New(slackapp.Options{
		Config:         cfg,
		Secrets:        a.Secrets(),
		Redactor:       a.Redactor(),
		Pool:           a.Pool(),
		Logger:         log,
		Metrics:        a.Telemetry().Registry(),
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the Slack app: %w", err)
	}

	if err := h.SetListeners(cache.Current().ListenerConfigs()); err != nil {
		return fmt.Errorf("use the runtime listeners: %w", err)
	}

	cache.Subscribe(func(_ context.Context, e *rules.Effective) error {
		if err := h.SetListeners(e.ListenerConfigs()); err != nil {
			return fmt.Errorf("apply the runtime listeners: %w", err)
		}

		return nil
	})

	lookup, err := slackroles.New(slackroles.Options{
		Slack:   cfg.Slack,
		Secrets: a.Secrets(),
		Pool:    a.Pool(),
		Logger:  a.Logger().With("component", "slackroles"),
	})
	if err != nil {
		return fmt.Errorf("set up Slack roles: %w", err)
	}

	commands := newSlackCommands(a, h, cache, lookup)

	buttons, err := newButtons(a, h, lookup)
	if err != nil {
		return fmt.Errorf("set up the Slack buttons: %w", err)
	}

	if cfg.Slack.Mode == config.SlackModeEvents {
		if err := a.RegisterRoutes(func(mux *http.ServeMux) {
			mux.Handle(slackapp.EventsPattern, h)
			mux.Handle(slackcmd.CommandsPattern, slackcmd.NewHTTPHandler(commands, h))
			mux.Handle(slackapp.InteractivityPattern, buttons)
		}); err != nil {
			return fmt.Errorf("register the Slack Events API route: %w", err)
		}

		return nil
	}

	socket := slackapp.NewSocket(slackapp.SocketOptions{
		Handler:  h,
		Pool:     a.Pool(),
		Secrets:  a.Secrets(),
		AppToken: cfg.Slack.AppTokenSecret,
		APIURL:   cfg.Slack.APIURL,
		Logger:   log,
		Commands: commands,

		Interactions: buttons,
	})

	if err := a.RegisterTask("slack-socket-mode", socket.Run); err != nil {
		return fmt.Errorf("register the Slack Socket Mode connection: %w", err)
	}

	return nil
}

// newButtons handles clicks on the buttons of Sidekick's messages, with
// sidekick_feedback_total, and stops watchers for Stop watching. Each
// click checks the clicker's role with lookup.
func newButtons(a *app.App, h *slackapp.Handler, lookup roles.Lookup) (*slackapp.Buttons, error) {
	metrics, err := feedback.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return nil, fmt.Errorf("set up feedback metrics: %w", err)
	}

	recorder, err := newWatchRecorder(a)
	if err != nil {
		return nil, fmt.Errorf("set up Stop watching: %w", err)
	}

	stopper, err := watch.NewStopper(watch.StopperOptions{Pool: a.Pool(), CloseGroup: grouper.EnqueueClose, Recorder: recorder})
	if err != nil {
		return nil, fmt.Errorf("set up Stop watching: %w", err)
	}

	return slackapp.NewButtons(slackapp.ButtonsOptions{
		Handler:  h,
		Pool:     a.Pool(),
		Roles:    lookup,
		Metrics:  metrics,
		Watchers: stopper,
		Logger:   a.Logger().With("component", "slackapp"),
	}), nil
}

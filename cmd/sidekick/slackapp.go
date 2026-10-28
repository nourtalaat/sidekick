package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/slackapp"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// registerSlackApp receives Slack events when the slack section lets the
// app receive them: over Socket Mode with the app token, or at
// POST /slack/events in events mode. Without either, Sidekick only posts
// to Slack.
func registerSlackApp(ctx context.Context, a *app.App) error {
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

	buttons, err := newButtons(a, h)
	if err != nil {
		return fmt.Errorf("set up the Slack buttons: %w", err)
	}

	if cfg.Slack.Mode == config.SlackModeEvents {
		if err := a.RegisterRoutes(func(mux *http.ServeMux) {
			mux.Handle(slackapp.EventsPattern, h)
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

		Interactions: buttons,
	})

	if err := a.RegisterTask("slack-socket-mode", socket.Run); err != nil {
		return fmt.Errorf("register the Slack Socket Mode connection: %w", err)
	}

	return nil
}

// newButtons handles clicks on the buttons of Sidekick's messages, with
// sidekick_feedback_total, and stops watchers for Stop watching. Every
// Slack user is a responder until roles can be set for Slack users.
func newButtons(a *app.App, h *slackapp.Handler) (*slackapp.Buttons, error) {
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
		Roles:    slackapp.EveryoneResponds{},
		Metrics:  metrics,
		Watchers: stopper,
		Logger:   a.Logger().With("component", "slackapp"),
	}), nil
}

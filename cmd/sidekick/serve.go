package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
)

// newServeCmd builds `sidekick serve`, which runs Sidekick until SIGTERM or
// SIGINT. regs are the plugin registries; the zero value means the global
// ones.
func newServeCmd(regs config.Registries, info buildinfo.Info) *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run Sidekick until it receives SIGTERM or SIGINT",
		Long: `Run Sidekick. It loads the config file and checks every secret it
references, migrates the database unless database.migrations is manual,
then serves webhooks at /hooks/<name> on server.listen, with the oidc
section the web UI login at /auth and the JSON API at /api/v1, and /metrics,
/healthz and /readyz on server.metricsListen. With the Slack app's app
token it receives Slack messages over Socket Mode, or at /slack/events
with slack.mode events. With a teams section it receives the Teams bot's
activities at /teams/messages. Each new event is routed
to the first route that matches it and joins the open group of the same
alert, or opens a new group with an investigation, which shows in the
route's outputs, such as a Slack thread. The
investigation asks the route's model, calls the sources it needs, and
posts its verdict to the same outputs. Watchers that the route defines,
or that the investigation starts, check on their own schedule whether
the service is back up. Once a day, records older than the retention
section allows are deleted. Logs go to standard output as JSON.

On SIGTERM or SIGINT it stops taking requests, hands back jobs it cannot
finish, and exits within ` + app.ShutdownGrace.String() + `. A second signal exits at once.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()

			// Once the first signal starts the shutdown, the next one gets
			// the default behaviour, which ends the process.
			context.AfterFunc(ctx, stop)

			return serve(ctx, cmd, app.Options{ConfigPath: configPath, Registries: regs, Build: info, LogOutput: cmd.OutOrStdout()})
		},
	}

	addConfigFlag(cmd, &configPath)

	return cmd
}

// serve makes the App and runs it until ctx ends. Problems in the config
// are printed one per line, as config validate prints them.
func serve(ctx context.Context, cmd *cobra.Command, opts app.Options) error {
	a, err := app.New(ctx, opts)
	if err != nil {
		var problems config.Errors
		if !errors.As(err, &problems) {
			return fmt.Errorf("start: %w", err)
		}

		cmd.SilenceErrors = true

		if werr := printProblems(cmd.ErrOrStderr(), err); werr != nil {
			return errors.Join(errInvalidConfig, werr)
		}

		return errInvalidConfig
	}

	if err := registerWebhooks(a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerAPI(ctx, a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	cache, err := registerRules(ctx, a)
	if err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerSlackApp(ctx, a, cache); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerTeamsApp(a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerRouter(a, cache); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerGrouper(a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerPublisher(a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerRetention(a); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	guard, err := newGuard(a)
	if err != nil {
		return errors.Join(fmt.Errorf("start: set up the tool guard: %w", err), a.Close())
	}

	watchers, err := registerWatchers(a, guard)
	if err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := registerInvestigator(a, guard, watchers); err != nil {
		return errors.Join(fmt.Errorf("start: %w", err), a.Close())
	}

	if err := a.Run(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/validate"
)

const (
	// readHeaderTimeout and readTimeout bound how long a client may take
	// to send its request.
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	// writeMargin is added to the longest a validation can take, waiting
	// for a slot included, to give the write timeout.
	writeMargin = 10 * time.Second
	// shutdownGrace is how long running validations may take to finish
	// after SIGTERM.
	shutdownGrace = validate.Timeout + writeMargin
)

// newServeCmd builds `sidekick-validate serve`, the sidecar mode.
func newServeCmd() *cobra.Command {
	var (
		tools  toolFlags
		listen string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve POST /validate until SIGTERM or SIGINT",
		Long: `Serve POST /validate, which takes {"format", "dir", "files"} and
answers with {"ok", "errors", "notChecked"}, and GET /healthz. The default
address is loopback only, so only the containers of the same pod reach it.

Validations get ` + validate.Timeout.String() + ` each, and at most ` + fmt.Sprint(validate.MaxConcurrent) + ` run at a time; a request that
finds no free slot within ` + validate.SlotWait.String() + ` gets 503.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, os.Interrupt)
			defer stop()

			return serve(ctx, &tools, listen)
		},
	}

	tools.add(cmd)
	cmd.Flags().StringVar(&listen, "listen", validate.ListenAddr, "address to listen on")

	return cmd
}

// serve runs the HTTP server until ctx ends, then lets running
// validations finish.
func serve(ctx context.Context, tools *toolFlags, listen string) error {
	log := newLogger()

	s, err := tools.service(log)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}

	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      validate.SlotWait + validate.Timeout + writeMargin,
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	log.Info("serving", "listen", ln.Addr().String(), "allow_downloads", tools.allowDownloads)

	select {
	case err := <-done:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down: %w", err)
	}

	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

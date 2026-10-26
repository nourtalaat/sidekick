package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Timeouts of both HTTP servers.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
)

// serve starts the main and the metrics listener and closes a.started.
// failed receives the error of a listener that stops on its own. The
// servers are returned in the order they are shut down: the main listener
// first, so the metrics listener answers probes until the end.
func (a *App) serve(ctx context.Context) (servers []*http.Server, failed <-chan error, err error) {
	servers = []*http.Server{
		a.newServer(a.cfg.Server.Listen, a.mux),
		a.newServer(a.cfg.Server.MetricsListen, a.metricsHandler()),
	}

	listeners := make([]net.Listener, 0, len(servers))

	for _, server := range servers {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", server.Addr)
		if err != nil {
			for _, l := range listeners {
				err = errors.Join(err, l.Close())
			}

			return nil, nil, fmt.Errorf("listen on %s: %w", server.Addr, err)
		}

		listeners = append(listeners, listener)
	}

	errs := make(chan error, len(servers))

	for i, server := range servers {
		go func() {
			if err := server.Serve(listeners[i]); !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("listener %s: %w", server.Addr, err)
			}
		}()
	}

	a.listenAddr, a.metricsAddr = listeners[0].Addr(), listeners[1].Addr()
	close(a.started)

	return servers, errs, nil
}

func (a *App) newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(a.log.With("listener", addr).Handler(), slog.LevelWarn),
	}
}

// metricsHandler serves /metrics, /healthz and /readyz.
func (a *App) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(a.telemetry.Registry(), promhttp.HandlerOpts{
		ErrorLog: slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
	}))
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("GET /readyz", readiness{cfg: a.cfg, pool: a.pool})

	return mux
}

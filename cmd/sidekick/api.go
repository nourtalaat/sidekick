package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// registerAPI serves the login endpoints under /auth and the JSON API
// under /api/v1 on the main listener, when the config has an oidc
// section. Without it, neither is served.
func registerAPI(ctx context.Context, a *app.App) error {
	cfg := a.Config()
	log := a.Logger().With("component", "api")

	if cfg.OIDC == nil {
		log.InfoContext(ctx, "the web UI login and the API are off: the config has no oidc section")

		return nil
	}

	sessions, err := auth.New(auth.Options{
		OIDC:      cfg.OIDC,
		PublicURL: cfg.Server.PublicURL,
		Secrets:   a.Secrets(),
		Pool:      a.Pool(),
		Audit:     audit.NewWriter(),
		Logger:    a.Logger().With("component", "auth"),
	})
	if err != nil {
		return fmt.Errorf("set up login: %w", err)
	}

	handler, err := api.New(api.Options{
		Auth:     sessions,
		Tokens:   sessions,
		Config:   cfg,
		Rules:    rules.NewStore(a.Pool(), cfg),
		Redactor: a.Redactor(),
		Pool:     a.Pool(),
		Audit:    audit.NewWriter(),
		Logger:   log,
	})
	if err != nil {
		return fmt.Errorf("set up the API: %w", err)
	}

	if err := a.RegisterRoutes(func(mux *http.ServeMux) {
		sessions.Register(mux)
		mux.Handle(api.BasePath+"/", handler)
	}); err != nil {
		return fmt.Errorf("register the API routes: %w", err)
	}

	return nil
}

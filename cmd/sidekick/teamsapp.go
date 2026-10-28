package main

import (
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/teamsapp"
)

// registerTeamsApp serves the Teams bot's messaging endpoint at
// POST /teams/messages on server.listen when the config has a teams
// section. The Azure Bot must reach it at a public https URL.
func registerTeamsApp(a *app.App) error {
	cfg := a.Config()
	if cfg.Teams == nil {
		return nil
	}

	h, err := teamsapp.New(teamsapp.Options{
		Config:         cfg,
		Secrets:        a.Secrets(),
		Redactor:       a.Redactor(),
		Pool:           a.Pool(),
		Logger:         a.Logger().With("component", "teamsapp"),
		Metrics:        a.Telemetry().Registry(),
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the Teams bot: %w", err)
	}

	if err := a.RegisterRoutes(func(mux *http.ServeMux) {
		mux.Handle(teamsapp.MessagesPattern, h)
	}); err != nil {
		return fmt.Errorf("register the Teams messaging endpoint: %w", err)
	}

	return nil
}

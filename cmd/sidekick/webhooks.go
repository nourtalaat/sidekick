package main

import (
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/intake/webhook"
)

// registerWebhooks serves the configured webhook endpoints at
// /hooks/{name} on the main listener.
func registerWebhooks(a *app.App) error {
	metrics, err := webhook.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up intake metrics: %w", err)
	}

	hooks, err := webhook.New(webhook.Options{
		Webhooks:       a.Config().Intake.Webhooks,
		Adapters:       a.Instances().Intakes,
		Secrets:        a.Secrets(),
		Redactor:       a.Redactor(),
		Pool:           a.Pool(),
		Logger:         a.Logger().With("component", "intake"),
		Metrics:        metrics,
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the webhook handler: %w", err)
	}

	if err := a.RegisterRoutes(func(mux *http.ServeMux) {
		mux.Handle(webhook.Pattern, hooks)
	}); err != nil {
		return fmt.Errorf("register webhook routes: %w", err)
	}

	return nil
}

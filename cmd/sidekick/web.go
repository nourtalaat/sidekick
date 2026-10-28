package main

import (
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/web"
)

// registerWeb serves the web UI on the main listener for every path that
// no other route owns. Without an oidc section, the UI paths answer with a
// page that says the UI needs one.
func registerWeb(a *app.App) error {
	ui, err := web.New(a.Config().OIDC != nil)
	if err != nil {
		return fmt.Errorf("set up the web UI: %w", err)
	}

	if err := a.RegisterRoutes(func(mux *http.ServeMux) {
		mux.Handle("/", ui)
	}); err != nil {
		return fmt.Errorf("register the web UI routes: %w", err)
	}

	return nil
}

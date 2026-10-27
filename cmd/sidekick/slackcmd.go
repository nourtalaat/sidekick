package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/slackapp"
	"github.com/nourtalaat/sidekick/internal/slackcmd"
	"github.com/nourtalaat/sidekick/internal/slackroles"
)

// newSlackCommands makes the handler of /sidekick commands, which saves
// runtime rules, checks roles from the slack section, and investigates
// linked messages through h.
func newSlackCommands(a *app.App, h *slackapp.Handler, cache *rules.Cache) (*slackcmd.Handler, error) {
	cfg := a.Config()

	lookup, err := slackroles.New(slackroles.Options{
		Slack:   cfg.Slack,
		Secrets: a.Secrets(),
		Pool:    a.Pool(),
		Logger:  a.Logger().With("component", "slackroles"),
	})
	if err != nil {
		return nil, fmt.Errorf("set up Slack roles: %w", err)
	}

	return slackcmd.New(slackcmd.Options{
		File:         cfg,
		Store:        rules.NewStore(a.Pool(), cfg),
		Rules:        cache,
		Roles:        lookup,
		Investigator: h,
		Logger:       a.Logger().With("component", "slackcmd"),
		Version:      buildinfo.Current().Version,
	}), nil
}

package main

import (
	"context"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/proposals"
)

// proposalTrigger returns the trigger that makes a strategy version with
// enough wrong feedback get a proposed change. It is off unless
// strategies.proposals.enabled is true, and also off, with a line in the
// log, when no model fills the summarize role, because nothing could draft
// the change.
func proposalTrigger(ctx context.Context, a *app.App) proposals.Trigger {
	p := a.Config().Strategies.Proposals
	if !p.Enabled {
		return proposals.Trigger{}
	}

	if a.Config().Model.Roles[model.RoleSummarize] == "" {
		a.Logger().WarnContext(ctx, "strategy proposals are enabled, but model.roles.summarize is not set, so no proposal will be drafted")

		return proposals.Trigger{}
	}

	return proposals.Trigger{Enabled: true, Threshold: p.WrongThreshold, Window: p.Window.Duration()}
}

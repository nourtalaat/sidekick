package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// registerActions builds the proposal service, the one place that stores
// the actions the model proposes, decides them and runs the approved ones,
// and registers its jobs: execute_action, which runs an approved proposal,
// and expire_proposals, which a task queues every minute in every replica.
// The service reads the routes in effect from cache whenever it checks
// what a route may do. The investigator, the API and the Slack buttons
// share the service it returns.
func registerActions(a *app.App, cache *rules.Cache) (*proposals.Service, error) {
	cfg := a.Config()

	metrics, err := proposals.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return nil, fmt.Errorf("set up the action metrics: %w", err)
	}

	log := a.Logger().With("component", "actions")

	svc, err := proposals.New(proposals.Options{
		Pool:                a.Pool(),
		Actions:             a.Instances().Actions,
		Policy:              rules.ActionPolicy{Cache: cache},
		Audit:               audit.NewWriter(),
		TTL:                 cfg.Actions.ProposalTTL,
		MaxPerInvestigation: cfg.Actions.MaxPerInvestigation,
		Redactor:            a.Redactor(),
		Metrics:             metrics,
		Logger:              log,
		TracerProvider:      a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return nil, fmt.Errorf("set up the action proposals: %w", err)
	}

	if err := a.RegisterJob(queue.KindExecuteAction, svc, queue.KindOptions{Long: true, OnDead: svc.OnDead}); err != nil {
		return nil, fmt.Errorf("register the action executor: %w", err)
	}

	if err := a.RegisterJob(queue.KindExpireProposals, queue.HandlerFunc(svc.HandleExpire), queue.KindOptions{}); err != nil {
		return nil, fmt.Errorf("register the proposal expiry: %w", err)
	}

	scheduler := proposals.NewScheduler(proposals.SchedulerOptions{Pool: a.Pool(), Logger: log})
	if err := a.RegisterTask("proposal-expiry-schedule", scheduler.Run); err != nil {
		return nil, fmt.Errorf("register the proposal expiry schedule: %w", err)
	}

	return svc, nil
}

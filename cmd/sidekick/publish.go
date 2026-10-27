package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerPublisher runs the publish jobs that show each investigation in
// its route's outputs.
func registerPublisher(a *app.App) error {
	h := publish.New(publish.Options{
		Pool:           a.Pool(),
		Sinks:          a.Instances().Outputs,
		Logger:         a.Logger().With("component", "publish"),
		TracerProvider: a.Telemetry().TracerProvider(),
	})

	if err := a.RegisterJob(queue.KindPublish, h, queue.KindOptions{Concurrency: publish.Concurrency}); err != nil {
		return fmt.Errorf("register the publisher: %w", err)
	}

	return registerNotices(a)
}

// registerNotices runs the notice jobs that tell a route's outputs about
// a daily cap or a rate limit.
func registerNotices(a *app.App) error {
	h := publish.NewNoticeHandler(publish.Options{
		Sinks:          a.Instances().Outputs,
		Logger:         a.Logger().With("component", "publish"),
		TracerProvider: a.Telemetry().TracerProvider(),
	})

	if err := a.RegisterJob(queue.KindNotice, h, queue.KindOptions{Concurrency: publish.NoticeConcurrency}); err != nil {
		return fmt.Errorf("register the notices: %w", err)
	}

	return nil
}

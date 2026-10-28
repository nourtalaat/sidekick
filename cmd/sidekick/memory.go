package main

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/model/instrument"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	memsource "github.com/nourtalaat/sidekick/internal/source/memory"
)

// registerMemoryVectors sets up vector memory when memory.vector.enabled is
// true, and returns its Vectors, or nil when it is off. It wraps the
// embeddings connector with its span and metrics, registers the job that
// embeds memory entries and a task that enqueues jobs for the entries that
// have no vector yet, and lets the memory sources search by meaning. Call
// it before the features that use the Vectors.
func registerMemoryVectors(a *app.App, modelMetrics *instrument.Metrics) (*memory.Vectors, error) {
	cfg := a.Config()
	if !cfg.Memory.Vector.Enabled {
		return nil, nil //nolint:nilnil // Nil Vectors means vector memory is off.
	}

	in := a.Instances()
	log := a.Logger().With("component", "memory")

	embedder := instrument.WrapEmbedder(in.Embedder, instrument.Options{
		Connector: config.EmbeddingsName,
		Provider:  cfg.Memory.Vector.Embeddings.Type,
		Metrics:   modelMetrics,
		Prices:    pricing.New(cfg.Model.PriceTable()),
		Tracer:    a.Telemetry().TracerProvider().Tracer(instrument.TracerName),
	})

	vectors := memory.NewVectors(embedder, in.EmbedderSettings.Retries, in.EmbedderSettings.Model, log)

	if err := a.RegisterJob(queue.KindEmbedMemory, memory.NewEmbedJob(a.Pool(), vectors, log), queue.KindOptions{}); err != nil {
		return nil, fmt.Errorf("register the memory embedding job: %w", err)
	}

	if err := a.RegisterTask("memory-backfill", func(ctx context.Context) error {
		backfillMemory(ctx, a, vectors)

		return nil
	}); err != nil {
		return nil, fmt.Errorf("register the memory backfill: %w", err)
	}

	for _, src := range in.Sources {
		if s, ok := src.(*memsource.Source); ok {
			s.UseVectors(vectors)
		}
	}

	return vectors, nil
}

// backfillMemory enqueues an embedding job for every memory entry that has
// no vector or one from another model. A failure is logged and does not stop
// Sidekick: the next start tries again.
func backfillMemory(ctx context.Context, a *app.App, vectors *memory.Vectors) {
	log := a.Logger().With("component", "memory")

	n, err := memory.Backfill(ctx, a.Pool(), vectors)
	if err != nil {
		log.ErrorContext(ctx, "cannot enqueue the embedding of memory entries", "error", err)

		return
	}

	log.InfoContext(ctx, "enqueued the embedding of memory entries", "entries", n, "model", vectors.Model())
}

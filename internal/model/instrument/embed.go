package instrument

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/model"
)

// EmbedSpanName is the name of the span of one embedding call.
const EmbedSpanName = "model.embed"

// operationEmbeddings is the gen_ai.operation.name of an embedding call.
const operationEmbeddings = "embeddings"

// attrTexts is the number of texts an embedding call holds.
const attrTexts = "sidekick.embed.texts"

// embedder is a model.Embedder with telemetry around Embed.
type embedder struct {
	next model.Embedder
	opts Options
}

var _ model.Embedder = (*embedder)(nil)

// Embed runs the wrapped embedder's Embed inside a model.embed span and
// records the metrics of the call, with the role embed. Errors keep the
// wrapped embedder's *model.Error, so errors.As and the retry helper still
// see it. The texts are never recorded.
func (e *embedder) Embed(ctx context.Context, texts []string) (model.EmbedResult, error) {
	configured := e.configuredModel()

	ctx, span := e.opts.Tracer.Start(ctx, EmbedSpanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrSystem, e.opts.Provider),
			attribute.String(attrOperation, operationEmbeddings),
			attribute.String(attrConnector, e.opts.Connector),
			attribute.String(attrRequestModel, configured),
			attribute.String(attrRole, string(model.RoleEmbed)),
			attribute.Int(attrTexts, len(texts)),
		))
	defer span.End()

	start := time.Now()
	res, err := e.next.Embed(ctx, texts)
	elapsed := time.Since(start)

	role := string(model.RoleEmbed)

	if err != nil {
		outcome := outcomeOf(err)
		span.SetAttributes(attribute.String(attrErrorType, outcome))
		span.SetStatus(codes.Error, outcome)
		e.opts.Metrics.recordCall(e.opts.Connector, configured, role, outcome, elapsed)

		return res, fmt.Errorf("instrumented embedding call: %w", err)
	}

	cost, priced := e.cost(configured, res.Usage)

	span.SetAttributes(
		attribute.Int64(attrInputTokens, res.Usage.PromptTokens()),
		attribute.String(attrRequestID, res.RequestID),
	)

	if priced {
		span.SetAttributes(attribute.Float64(attrCostUSD, cost))
	}

	e.opts.Metrics.recordCall(e.opts.Connector, configured, role, outcomeOK, elapsed)
	e.opts.Metrics.recordUsage(e.opts.Connector, configured, "", res.Usage, cost, priced)

	return res, nil
}

// Dimensions returns the dimensions of the wrapped embedder.
func (e *embedder) Dimensions() int {
	return e.next.Dimensions()
}

// ModelID returns the model ID of the wrapped embedder, when it has one.
func (e *embedder) ModelID() string {
	return e.configuredModel()
}

// configuredModel returns the model ID the wrapped embedder is configured
// for, or "" when it cannot say.
func (e *embedder) configuredModel() string {
	if m, ok := e.next.(ModelIDer); ok {
		return m.ModelID()
	}

	return ""
}

// cost returns the cost of usage in US dollars, and false when there is no
// price for the model.
func (e *embedder) cost(modelID string, usage model.Usage) (float64, bool) {
	if e.opts.Prices == nil {
		return 0, false
	}

	p, ok := e.opts.Prices.Lookup(modelID)
	if !ok {
		return 0, false
	}

	return float64(p.Cost(usage)) / microDollarsPerUSD, true
}

// WrapEmbedder returns e with the model.embed span and the model metrics
// around every Embed call. The calls count as role embed in
// sidekick_model_calls_total, and their input tokens and cost go into
// sidekick_model_tokens_total and sidekick_model_cost_usd_total. Wrap the
// embedder, then retry over the wrapped one, so each attempt is a span and
// a count of its own.
func WrapEmbedder(e model.Embedder, opts Options) model.Embedder {
	if opts.Tracer == nil {
		opts.Tracer = otel.Tracer(TracerName)
	}

	return &embedder{next: e, opts: opts}
}

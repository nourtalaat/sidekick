// Package instrument wraps a model connector with the model.generate trace
// span and the model call metrics. The app wraps every connector it builds,
// so each call is recorded the same way whatever the provider. Prompt and
// response content never become span attributes or metric labels.
package instrument

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/model"
)

// SpanName is the name of the span of one model call.
const SpanName = "model.generate"

// TracerName names the tracer of this package. Pass it to the tracer
// provider when setting Options.Tracer.
const TracerName = "github.com/nourtalaat/sidekick/internal/model/instrument"

// outcomeOK is the outcome label of a successful call. Failed calls use
// their model.ErrorKind, or outcomeError for errors that are not a
// *model.Error.
const (
	outcomeOK    = "ok"
	outcomeError = "error"
)

// microDollarsPerUSD converts costs from micro-dollars to US dollars.
const microDollarsPerUSD = 1_000_000

// Span attributes. The gen_ai ones follow the OpenTelemetry GenAI
// conventions.
const (
	attrSystem        = "gen_ai.system"
	attrOperation     = "gen_ai.operation.name"
	attrRequestModel  = "gen_ai.request.model"
	attrMaxTokens     = "gen_ai.request.max_tokens" //nolint:gosec // An attribute name, not a credential.
	attrResponseModel = "gen_ai.response.model"
	attrFinishReasons = "gen_ai.response.finish_reasons"
	attrInputTokens   = "gen_ai.usage.input_tokens"  //nolint:gosec // An attribute name, not a credential.
	attrOutputTokens  = "gen_ai.usage.output_tokens" //nolint:gosec // An attribute name, not a credential.
	attrErrorType     = "error.type"
	attrConnector     = "sidekick.connector"
	attrRole          = "sidekick.role"
	attrRoute         = "sidekick.route"
	attrCacheRead     = "sidekick.usage.cache_read_tokens"
	attrCacheWrite    = "sidekick.usage.cache_write_tokens"
	attrCostUSD       = "sidekick.cost_usd"
	attrRequestID     = "sidekick.request_id"

	operationChat = "chat"
)

// Prices looks up the price of a model. *pricing.Table implements it.
type Prices interface {
	Lookup(modelID string) (model.Price, bool)
}

// Options say how a wrapped connector is labeled and where it records.
type Options struct {
	// Connector is the name of the connector instance, such as "claude".
	Connector string
	// Provider is the connector type, such as "anthropic". It is the
	// gen_ai.system span attribute.
	Provider string
	// Metrics records the model call metrics. Nil records none.
	Metrics *Metrics
	// Prices prices successful calls for the cost metric and the
	// sidekick.cost_usd attribute. Nil leaves cost out.
	Prices Prices
	// Tracer starts the spans. Nil uses the global tracer provider.
	Tracer trace.Tracer
}

// ModelIDer is implemented by connectors that can name the model they are
// configured for. Wrap uses it for the model label, the price lookup and
// gen_ai.request.model; without it, the model the response names is used.
type ModelIDer interface {
	ModelID() string
}

// connector is a model.Connector with telemetry around Generate.
type connector struct {
	next model.Connector
	opts Options
}

// Capabilities returns the capabilities of the wrapped connector.
func (c *connector) Capabilities() model.Capabilities {
	return c.next.Capabilities()
}

// Generate runs the wrapped connector's Generate inside a model.generate
// span and records the metrics of the call. Errors keep the wrapped
// connector's *model.Error, so errors.As and the retry helper still see it.
func (c *connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	role := req.Metadata[model.MetadataRole]
	route := req.Metadata[model.MetadataRoute]
	configured := c.configuredModel()

	ctx, span := c.opts.Tracer.Start(ctx, SpanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String(attrSystem, c.opts.Provider),
			attribute.String(attrOperation, operationChat),
			attribute.String(attrConnector, c.opts.Connector),
			attribute.String(attrRequestModel, configured),
			attribute.String(attrRole, role),
			attribute.String(attrRoute, route),
		))
	defer span.End()

	if req.MaxTokens > 0 {
		span.SetAttributes(attribute.Int(attrMaxTokens, req.MaxTokens))
	}

	start := time.Now()
	resp, err := c.next.Generate(ctx, req)
	elapsed := time.Since(start)

	modelID := configured
	if modelID == "" {
		modelID = resp.Model
	}

	if err != nil {
		outcome := outcomeOf(err)
		span.SetAttributes(attribute.String(attrErrorType, outcome))
		span.SetStatus(codes.Error, outcome)
		c.opts.Metrics.recordCall(c.opts.Connector, modelID, role, outcome, elapsed)

		return resp, fmt.Errorf("instrumented call: %w", err)
	}

	cost, priced := c.cost(modelID, resp.Usage)

	span.SetAttributes(
		attribute.String(attrResponseModel, resp.Model),
		attribute.StringSlice(attrFinishReasons, []string{string(resp.StopReason)}),
		attribute.Int64(attrInputTokens, resp.Usage.PromptTokens()),
		attribute.Int64(attrOutputTokens, resp.Usage.OutputTokens),
		attribute.Int64(attrCacheRead, resp.Usage.CacheReadTokens),
		attribute.Int64(attrCacheWrite, resp.Usage.CacheWriteTokens),
		attribute.String(attrRequestID, resp.RequestID),
	)

	if priced {
		span.SetAttributes(attribute.Float64(attrCostUSD, cost))
	}

	c.opts.Metrics.recordCall(c.opts.Connector, modelID, role, outcomeOK, elapsed)
	c.opts.Metrics.recordUsage(c.opts.Connector, modelID, route, resp.Usage, cost, priced)

	return resp, nil
}

// configuredModel returns the model ID the wrapped connector is configured
// for, or "" when it cannot say.
func (c *connector) configuredModel() string {
	if m, ok := c.next.(ModelIDer); ok {
		return m.ModelID()
	}

	return ""
}

// cost returns the cost of usage in US dollars, and false when there is no
// price for the model.
func (c *connector) cost(modelID string, usage model.Usage) (float64, bool) {
	if c.opts.Prices == nil {
		return 0, false
	}

	p, ok := c.opts.Prices.Lookup(modelID)
	if !ok {
		return 0, false
	}

	return float64(p.Cost(usage)) / microDollarsPerUSD, true
}

// recordCall counts one call and its duration.
func (m *Metrics) recordCall(connector, modelID, role, outcome string, elapsed time.Duration) {
	if m == nil {
		return
	}

	m.calls.WithLabelValues(connector, modelID, role, outcome).Inc()
	m.duration.WithLabelValues(connector, modelID).Observe(elapsed.Seconds())
}

// recordUsage adds the tokens of a successful call, and its cost when it
// has a price.
func (m *Metrics) recordUsage(connector, modelID, route string, usage model.Usage, cost float64, priced bool) {
	if m == nil {
		return
	}

	for kind, n := range map[string]int64{
		tokensInput:      usage.InputTokens,
		tokensOutput:     usage.OutputTokens,
		tokensCacheRead:  usage.CacheReadTokens,
		tokensCacheWrite: usage.CacheWriteTokens,
	} {
		m.tokens.WithLabelValues(connector, modelID, kind).Add(float64(n))
	}

	if priced {
		m.cost.WithLabelValues(connector, modelID, route).Add(cost)
	}
}

// Wrap returns c with the model.generate span and the model metrics around
// every Generate call. Wrap the connector, then retry over the wrapped one
// with model.Generate, so each attempt is a span and a count of its own.
func Wrap(c model.Connector, opts Options) model.Connector {
	if opts.Tracer == nil {
		opts.Tracer = otel.Tracer(TracerName)
	}

	return &connector{next: c, opts: opts}
}

// outcomeOf returns the outcome label of a failed call.
func outcomeOf(err error) string {
	var merr *model.Error
	if errors.As(err, &merr) {
		return string(merr.Kind)
	}

	return outcomeError
}

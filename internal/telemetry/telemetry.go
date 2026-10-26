// Package telemetry sets up the logs, metrics and traces of a Sidekick
// process: JSON logs through log/slog that carry the trace and span IDs of
// the current span, a Prometheus registry with sidekick_build_info, and an
// OpenTelemetry tracer provider that exports over OTLP when
// telemetry.otlpEndpoint is set.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
)

// ServiceName is the service.name of Sidekick's traces.
const ServiceName = "sidekick"

// Log fields that the logger adds to every line written inside a span.
const (
	FieldTraceID = "trace_id"
	FieldSpanID  = "span_id"
)

// Telemetry holds the logger, the metrics registry and the tracer provider
// of one process. Make it with New and call Shutdown when the process stops.
type Telemetry struct {
	logger   *slog.Logger
	registry *prometheus.Registry
	tracers  *sdktrace.TracerProvider
}

// Options change where New sends its output. The zero value is what the
// binary uses.
type Options struct {
	// Build labels sidekick_build_info and the traces' service.version.
	Build buildinfo.Info
	// LogOutput receives the JSON logs. Nil means standard output.
	LogOutput io.Writer
}

// New sets up telemetry from cfg. Traces are always recorded, so that log
// lines inside a span carry its IDs, but they leave the process only when
// cfg.OTLPEndpoint is set. They are sent with the OTLP gRPC exporter, which
// a collector receives on port 4317; an http:// endpoint is sent without
// TLS.
func New(ctx context.Context, cfg config.Telemetry, opts Options) (*Telemetry, error) {
	out := opts.LogOutput
	if out == nil {
		out = os.Stdout
	}

	registry, err := NewRegistry(opts.Build)
	if err != nil {
		return nil, fmt.Errorf("set up metrics: %w", err)
	}

	tracers, err := newTracerProvider(ctx, cfg, opts.Build)
	if err != nil {
		return nil, fmt.Errorf("set up traces: %w", err)
	}

	return &Telemetry{
		logger:   NewLogger(out, cfg.Level()),
		registry: registry,
		tracers:  tracers,
	}, nil
}

// Logger returns the process's logger. Components add their name with
// Logger().With("component", name).
func (t *Telemetry) Logger() *slog.Logger {
	return t.logger
}

// Registry returns the Prometheus registry that /metrics serves. Components
// register their metrics on it.
func (t *Telemetry) Registry() *prometheus.Registry {
	return t.registry
}

// TracerProvider returns the provider components get their tracers from.
func (t *Telemetry) TracerProvider() trace.TracerProvider {
	return t.tracers
}

// Shutdown sends the spans that are still buffered and stops the trace
// exporter. It returns when they are sent or ctx ends.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if err := t.tracers.Shutdown(ctx); err != nil {
		return fmt.Errorf("shut down tracing: %w", err)
	}

	return nil
}

// Logger returns a JSON logger that writes to standard output at the level
// cfg sets, with the trace and span IDs on lines written inside a span.
func Logger(cfg config.Telemetry) *slog.Logger {
	return NewLogger(os.Stdout, cfg.Level())
}

// NewLogger returns a JSON logger that writes to w at level and adds
// trace_id and span_id to every line whose context holds a valid span.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	return slog.New(traceHandler{Handler: slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})})
}

// NewRegistry returns a Prometheus registry with the Go runtime and process
// collectors and sidekick_build_info, a gauge that is always 1, labelled
// with the version and commit of the build.
func NewRegistry(info buildinfo.Info) (*prometheus.Registry, error) {
	registry := prometheus.NewRegistry()

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "sidekick_build_info",
		Help:        "Always 1; its labels give the version and commit of the running build.",
		ConstLabels: prometheus.Labels{"version": info.Version, "commit": info.Commit},
	})
	buildInfo.Set(1)

	for _, c := range []prometheus.Collector{
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
	} {
		if err := registry.Register(c); err != nil {
			return nil, fmt.Errorf("register metrics: %w", err)
		}
	}

	return registry, nil
}

// traceHandler adds the IDs of the span in a record's context to the
// record. Inside a group, the IDs land in that group.
type traceHandler struct {
	slog.Handler
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String(FieldTraceID, sc.TraceID().String()), slog.String(FieldSpanID, sc.SpanID().String()))
	}

	if err := h.Handler.Handle(ctx, r); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}

	return nil
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{Handler: h.Handler.WithGroup(name)}
}

// newTracerProvider returns a tracer provider that samples every new trace
// and follows the caller's decision for continued ones, and exports over
// OTLP gRPC when cfg.OTLPEndpoint is set.
func newTracerProvider(ctx context.Context, cfg config.Telemetry, info buildinfo.Info) (*sdktrace.TracerProvider, error) {
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(ServiceName), semconv.ServiceVersion(info.Version)),
		resource.WithFromEnv(),
	)
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("describe the service: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}

	if cfg.OTLPEndpoint != "" {
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, fmt.Errorf("create the OTLP exporter for %s: %w", cfg.OTLPEndpoint, err)
		}

		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	return sdktrace.NewTracerProvider(opts...), nil
}

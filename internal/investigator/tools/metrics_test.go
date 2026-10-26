package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/source"
)

// secretText is tool input and output that must never reach telemetry.
const secretText = "password hunter2"

func TestCallTelemetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		call        model.ToolCall
		result      source.Result
		err         error
		noCall      bool
		wantTool    string
		wantSource  string
		wantOutcome string
		wantMetric  bool
		wantStatus  codes.Code
	}{
		{name: "ok", result: source.Result{Text: secretText}, wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeOK, wantMetric: true},
		{name: "no data", result: source.Result{NoData: true}, wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeNoData, wantMetric: true},
		{
			name: "tool error", err: &source.ToolError{Message: secretText},
			wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeError, wantMetric: true, wantStatus: codes.Error,
		},
		{
			name: "unavailable", err: source.ErrUnavailable,
			wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeUnavailable, wantMetric: true, wantStatus: codes.Error,
		},
		{
			name: "timeout", err: context.DeadlineExceeded,
			wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeTimeout, wantMetric: true, wantStatus: codes.Error,
		},
		{
			name: "other error", err: errors.New(secretText),
			wantSource: "signoz", wantTool: "search_logs", wantOutcome: outcomeError, wantMetric: true, wantStatus: codes.Error,
		},
		{
			name:        "invalid input has a span and no metric",
			call:        model.ToolCall{ID: "call-1", Name: "signoz__search_logs", Input: json.RawMessage(`{"query":1}`)},
			noCall:      true,
			wantSource:  "signoz",
			wantTool:    "search_logs",
			wantOutcome: string(KindInvalidInput),
			wantStatus:  codes.Error,
		},
		{
			name:        "an unknown tool has a span and no metric",
			call:        model.ToolCall{ID: "call-1", Name: "signoz__nope", Input: goodInput},
			noCall:      true,
			wantTool:    "signoz__nope",
			wantOutcome: string(KindUnknownTool),
			wantStatus:  codes.Error,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			m := source.NewMockSource(ctrl)
			m.EXPECT().Tools(gomock.Any()).Return([]source.Tool{searchTool}, nil)

			if !tt.noCall {
				m.EXPECT().Call(gomock.Any(), gomock.Any()).Return(tt.result, tt.err)
			}

			reg := prometheus.NewPedanticRegistry()
			metrics, err := NewMetrics(reg)
			require.NoError(t, err)

			spans := tracetest.NewSpanRecorder()
			tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")

			g, err := New([]Source{{Name: "signoz", Source: m}}, Options{
				Redactor: newRedactor(t), Logger: slog.New(slog.DiscardHandler), Metrics: metrics, Tracer: tracer,
			})
			require.NoError(t, err)

			call := tt.call
			if call.Name == "" {
				call = model.ToolCall{ID: "call-1", Name: "signoz__search_logs", Input: goodInput}
			}

			g.Start(t.Context(), "inv-1", nil).Call(t.Context(), call)

			wantCount := 0.0
			if tt.wantMetric {
				wantCount = 1
			}

			require.InDelta(t, wantCount, testutil.ToFloat64(metrics.calls.WithLabelValues("signoz", "search_logs", tt.wantOutcome)), 0)

			ended := spans.Ended()
			require.Len(t, ended, 1)
			require.Equal(t, SpanName, ended[0].Name())
			require.Equal(t, tt.wantStatus, ended[0].Status().Code)

			attrs := map[attribute.Key]attribute.Value{}
			for _, kv := range ended[0].Attributes() {
				attrs[kv.Key] = kv.Value

				require.NotContains(t, kv.Value.String(), "hunter2")
			}

			require.Equal(t, map[attribute.Key]attribute.Value{
				attrSource:  attribute.StringValue(tt.wantSource),
				attrTool:    attribute.StringValue(tt.wantTool),
				attrOutcome: attribute.StringValue(tt.wantOutcome),
			}, attrs)
		})
	}
}

func TestSourceUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		connected bool
		failures  int
		want      string
	}{
		{name: "connected with a closed circuit", connected: true, want: `sidekick_source_up{source="signoz"} 1`},
		{name: "not connected", connected: false, want: `sidekick_source_up{source="signoz"} 0`},
		{name: "connected with an open circuit", connected: true, failures: breakerFailures, want: `sidekick_source_up{source="signoz"} 0`},
		{name: "a few failures keep it up", connected: true, failures: breakerFailures - 1, want: `sidekick_source_up{source="signoz"} 1`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			metrics, err := NewMetrics(reg)
			require.NoError(t, err)

			src := &upSource{}
			src.connected.Store(tt.connected)

			g, err := New([]Source{{Name: "signoz", Source: src}, {Name: "plain", Source: noopSource{}}}, Options{Redactor: newRedactor(t), Metrics: metrics})
			require.NoError(t, err)

			for range tt.failures {
				g.sources["signoz"].breaker.done(callFailed, false)
			}

			want := "# HELP sidekick_source_up Whether a source can take calls: 1 when it is connected and its circuit breaker is closed, 0 otherwise.\n" +
				"# TYPE sidekick_source_up gauge\n" +
				`sidekick_source_up{source="plain"} 1` + "\n" +
				tt.want + "\n"
			require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(want), MetricSourceUp))
		})
	}
}

func TestNewMetricsRegistersOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a second registration fails"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			_, err := NewMetrics(reg)
			require.NoError(t, err)

			_, err = NewMetrics(reg)
			require.Error(t, err, tt.name)
		})
	}
}

func TestNilMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a nil Metrics records nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var m *Metrics

			require.NotPanics(t, func() {
				m.recordCall("signoz", "search_logs", outcomeOK, time.Second)
				m.watch(nil)
			}, tt.name)
		})
	}
}

// upSource is a source that says whether it is connected.
type upSource struct {
	noopSource

	connected atomic.Bool
}

func (s *upSource) Up() bool { return s.connected.Load() }

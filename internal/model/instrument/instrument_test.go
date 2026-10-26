package instrument

import (
	"errors"
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
)

// secretPrompt is prompt and response text that must never reach telemetry.
const secretPrompt = "the database password is hunter2"

// namedConnector is a mock connector that also names its model.
type namedConnector struct {
	*model.MockConnector

	modelID string
}

func (n namedConnector) ModelID() string { return n.modelID }

// fixedPrices prices every model the same.
type fixedPrices struct{ price model.Price }

func (f fixedPrices) Lookup(string) (model.Price, bool) { return f.price, true }

func TestWrapGenerate(t *testing.T) {
	t.Parallel()

	usage := model.Usage{InputTokens: 1_000, OutputTokens: 2_000, CacheReadTokens: 10_000, CacheWriteTokens: 500}
	resp := model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: secretPrompt}}},
		StopReason: model.StopEndTurn,
		Usage:      usage,
		Model:      "claude-opus-5-5",
		RequestID:  "req_1",
	}
	// One dollar per million tokens of every kind: the usage above costs
	// 13,500 micro-dollars.
	price := model.Price{InputPerMTok: 1_000_000, OutputPerMTok: 1_000_000, CacheReadPerMTok: 1_000_000, CacheWritePerMTok: 1_000_000}
	rateLimited := &model.Error{Kind: model.ErrRateLimited, Err: errors.New("429")}

	tests := []struct {
		name        string
		configured  string
		prices      Prices
		err         error
		wantModel   string
		wantOutcome string
		wantStatus  codes.Code
		wantCost    float64
		wantAttrs   map[attribute.Key]attribute.Value
	}{
		{
			name:        "success with a price",
			configured:  "claude-opus-5-5",
			prices:      fixedPrices{price: price},
			wantModel:   "claude-opus-5-5",
			wantOutcome: outcomeOK,
			wantStatus:  codes.Unset,
			wantCost:    0.0135,
			wantAttrs: map[attribute.Key]attribute.Value{
				attrInputTokens:   attribute.Int64Value(11_500),
				attrOutputTokens:  attribute.Int64Value(2_000),
				attrResponseModel: attribute.StringValue("claude-opus-5-5"),
				attrCostUSD:       attribute.Float64Value(0.0135),
				attrRequestID:     attribute.StringValue("req_1"),
			},
		},
		{
			name:        "success without a price records no cost",
			configured:  "claude-opus-5-5",
			wantModel:   "claude-opus-5-5",
			wantOutcome: outcomeOK,
			wantStatus:  codes.Unset,
		},
		{
			name:        "without a configured model the response names it",
			prices:      fixedPrices{price: price},
			wantModel:   "claude-opus-5-5",
			wantOutcome: outcomeOK,
			wantStatus:  codes.Unset,
			wantCost:    0.0135,
		},
		{
			name:        "a model error is labeled with its kind",
			configured:  "claude-opus-5-5",
			prices:      fixedPrices{price: price},
			err:         rateLimited,
			wantModel:   "claude-opus-5-5",
			wantOutcome: string(model.ErrRateLimited),
			wantStatus:  codes.Error,
			wantAttrs:   map[attribute.Key]attribute.Value{attrErrorType: attribute.StringValue("rate_limited")},
		},
		{
			name:        "another error is labeled error",
			configured:  "claude-opus-5-5",
			err:         errors.New("boom"),
			wantModel:   "claude-opus-5-5",
			wantOutcome: outcomeError,
			wantStatus:  codes.Error,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mock := model.NewMockConnector(ctrl)

			var next model.Connector = mock
			if tt.configured != "" {
				next = namedConnector{MockConnector: mock, modelID: tt.configured}
			}

			if tt.err != nil {
				mock.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(model.Response{}, tt.err)
			} else {
				mock.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(resp, nil)
			}

			reg := prometheus.NewPedanticRegistry()
			metrics, err := NewMetrics(reg)
			require.NoError(t, err)

			spans := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

			c := Wrap(next, Options{
				Connector: "claude",
				Provider:  "anthropic",
				Metrics:   metrics,
				Prices:    tt.prices,
				Tracer:    tp.Tracer("test"),
			})

			req := model.Request{
				System:    []model.Text{{Text: secretPrompt}},
				Messages:  []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: secretPrompt}}}},
				MaxTokens: 4_000,
				Metadata:  map[string]string{model.MetadataRole: "investigate", model.MetadataRoute: "default"},
			}

			got, err := c.Generate(t.Context(), req)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, resp, got)
			}

			require.InDelta(t, 1, testutil.ToFloat64(metrics.calls.WithLabelValues("claude", tt.wantModel, "investigate", tt.wantOutcome)), 0)
			require.Equal(t, 1, testutil.CollectAndCount(metrics.duration))

			if tt.err == nil {
				require.InDelta(t, 1_000, testutil.ToFloat64(metrics.tokens.WithLabelValues("claude", tt.wantModel, tokensInput)), 0)
				require.InDelta(t, 2_000, testutil.ToFloat64(metrics.tokens.WithLabelValues("claude", tt.wantModel, tokensOutput)), 0)
				require.InDelta(t, 10_000, testutil.ToFloat64(metrics.tokens.WithLabelValues("claude", tt.wantModel, tokensCacheRead)), 0)
				require.InDelta(t, 500, testutil.ToFloat64(metrics.tokens.WithLabelValues("claude", tt.wantModel, tokensCacheWrite)), 0)
			} else {
				require.Equal(t, 0, testutil.CollectAndCount(metrics.tokens))
			}

			if tt.wantCost > 0 {
				require.InDelta(t, tt.wantCost, testutil.ToFloat64(metrics.cost.WithLabelValues("claude", tt.wantModel, "default")), 1e-12)
			} else {
				require.Equal(t, 0, testutil.CollectAndCount(metrics.cost))
			}

			ended := spans.Ended()
			require.Len(t, ended, 1)
			span := ended[0]
			require.Equal(t, SpanName, span.Name())
			require.Equal(t, tt.wantStatus, span.Status().Code)

			attrs := map[attribute.Key]attribute.Value{}
			for _, kv := range span.Attributes() {
				attrs[kv.Key] = kv.Value
				require.NotContains(t, kv.Value.String(), "hunter2", "attribute %s carries content", kv.Key)
			}

			require.Equal(t, attribute.StringValue("anthropic"), attrs[attrSystem])
			require.Equal(t, attribute.StringValue("claude"), attrs[attrConnector])
			require.Equal(t, attribute.StringValue("investigate"), attrs[attrRole])
			require.Equal(t, attribute.StringValue("default"), attrs[attrRoute])
			require.Equal(t, attribute.IntValue(4_000), attrs[attrMaxTokens])

			for k, v := range tt.wantAttrs {
				require.Equal(t, v, attrs[k], "attribute %s", k)
			}

			if tt.wantCost == 0 {
				require.NotContains(t, attrs, attribute.Key(attrCostUSD))
			}
		})
	}
}

func TestWrapCapabilities(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := model.NewMockConnector(ctrl)
	caps := model.Capabilities{ToolCalling: true, MaxOutputTokens: 128_000}
	mock.EXPECT().Capabilities().Return(caps)

	require.Equal(t, caps, Wrap(mock, Options{}).Capabilities())
}

func TestWrapWithoutMetrics(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := model.NewMockConnector(ctrl)
	mock.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(model.Response{Model: "m"}, nil)

	_, err := Wrap(mock, Options{Prices: fixedPrices{}}).Generate(t.Context(), model.Request{})
	require.NoError(t, err)
}

func TestNewMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		twice bool
	}{
		{name: "registers every metric"},
		{name: "registering twice fails", twice: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewPedanticRegistry()
			m, err := NewMetrics(reg)
			require.NoError(t, err)

			if tt.twice {
				_, err = NewMetrics(reg)
				require.Error(t, err)

				return
			}

			m.recordCall("claude", "m", "investigate", outcomeOK, time.Second)
			m.recordUsage("claude", "m", "default", model.Usage{InputTokens: 1}, 1, true)

			families, err := reg.Gather()
			require.NoError(t, err)

			var names []string
			for _, f := range families {
				names = append(names, f.GetName())
			}

			require.Equal(t, []string{MetricDuration, MetricCalls, MetricCost, MetricTokens}, names)
		})
	}
}

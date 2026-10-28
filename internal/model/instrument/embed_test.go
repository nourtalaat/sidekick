package instrument

import (
	"errors"
	"testing"

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

// namedEmbedder is a mock embedder that also names its model.
type namedEmbedder struct {
	*model.MockEmbedder

	modelID string
}

func (n namedEmbedder) ModelID() string { return n.modelID }

func TestWrapEmbed(t *testing.T) {
	t.Parallel()

	res := model.EmbedResult{Vectors: [][]float32{{1, 2}, {3, 4}}, Usage: model.Usage{InputTokens: 2_000}, RequestID: "req_e1"}
	// Two dollars per million input tokens: the usage above costs 4,000
	// micro-dollars.
	price := model.Price{InputPerMTok: 2_000_000}
	rateLimited := &model.Error{Kind: model.ErrRateLimited, Err: errors.New("429")}

	tests := []struct {
		name        string
		prices      Prices
		err         error
		wantOutcome string
		wantStatus  codes.Code
		wantCost    float64
	}{
		{name: "success with a price", prices: fixedPrices{price: price}, wantOutcome: outcomeOK, wantStatus: codes.Unset, wantCost: 0.004},
		{name: "success without a price records no cost", wantOutcome: outcomeOK, wantStatus: codes.Unset},
		{name: "a model error is labeled with its kind", err: rateLimited, wantOutcome: string(model.ErrRateLimited), wantStatus: codes.Error},
		{name: "another error is labeled error", err: errors.New("boom"), wantOutcome: outcomeError, wantStatus: codes.Error},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mock := model.NewMockEmbedder(ctrl)
			mock.EXPECT().Dimensions().Return(2).AnyTimes()

			if tt.err != nil {
				mock.EXPECT().Embed(gomock.Any(), gomock.Any()).Return(model.EmbedResult{}, tt.err)
			} else {
				mock.EXPECT().Embed(gomock.Any(), gomock.Any()).Return(res, nil)
			}

			reg := prometheus.NewPedanticRegistry()
			metrics, err := NewMetrics(reg)
			require.NoError(t, err)

			spans := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

			e := WrapEmbedder(namedEmbedder{MockEmbedder: mock, modelID: "text-embedding-3-small"}, Options{
				Connector: "memory-embeddings",
				Provider:  "openai",
				Metrics:   metrics,
				Prices:    tt.prices,
				Tracer:    tp.Tracer("test"),
			})

			require.Equal(t, 2, e.Dimensions())

			got, err := e.Embed(t.Context(), []string{secretPrompt, secretPrompt})
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
			} else {
				require.NoError(t, err)
				require.Equal(t, res, got)
			}

			require.InDelta(t, 1, testutil.ToFloat64(metrics.calls.WithLabelValues("memory-embeddings", "text-embedding-3-small", string(model.RoleEmbed), tt.wantOutcome)), 0)

			if tt.err == nil {
				require.InDelta(t, 2_000, testutil.ToFloat64(metrics.tokens.WithLabelValues("memory-embeddings", "text-embedding-3-small", tokensInput)), 0)
			} else {
				require.Equal(t, 0, testutil.CollectAndCount(metrics.tokens))
			}

			if tt.wantCost > 0 {
				require.InDelta(t, tt.wantCost, testutil.ToFloat64(metrics.cost.WithLabelValues("memory-embeddings", "text-embedding-3-small", "")), 1e-12)
			} else {
				require.Equal(t, 0, testutil.CollectAndCount(metrics.cost))
			}

			ended := spans.Ended()
			require.Len(t, ended, 1)
			require.Equal(t, EmbedSpanName, ended[0].Name())
			require.Equal(t, tt.wantStatus, ended[0].Status().Code)

			attrs := map[attribute.Key]attribute.Value{}
			for _, kv := range ended[0].Attributes() {
				attrs[kv.Key] = kv.Value
				require.NotContains(t, kv.Value.String(), "hunter2", "attribute %s carries content", kv.Key)
			}

			require.Equal(t, attribute.StringValue("openai"), attrs[attrSystem])
			require.Equal(t, attribute.StringValue(operationEmbeddings), attrs[attrOperation])
			require.Equal(t, attribute.StringValue("embed"), attrs[attrRole])
			require.Equal(t, attribute.IntValue(2), attrs[attrTexts])

			if tt.err == nil {
				require.Equal(t, attribute.Int64Value(2_000), attrs[attrInputTokens])
				require.Equal(t, attribute.StringValue("req_e1"), attrs[attrRequestID])
			}

			if tt.wantCost == 0 {
				require.NotContains(t, attrs, attribute.Key(attrCostUSD))
			}
		})
	}
}

func TestWrapEmbedderWithoutMetrics(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := model.NewMockEmbedder(ctrl)
	mock.EXPECT().Embed(gomock.Any(), gomock.Any()).Return(model.EmbedResult{}, nil)

	e := WrapEmbedder(mock, Options{Prices: fixedPrices{}})

	_, err := e.Embed(t.Context(), []string{"a"})
	require.NoError(t, err)

	named, ok := e.(ModelIDer)
	require.True(t, ok)
	require.Empty(t, named.ModelID())
}

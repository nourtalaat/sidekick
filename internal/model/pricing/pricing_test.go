package pricing

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestTableCost(t *testing.T) {
	t.Parallel()

	override := model.Price{InputPerMTok: 1 * usd, OutputPerMTok: 2 * usd, CacheReadPerMTok: usd / 10, CacheWritePerMTok: 3 * usd}

	tests := []struct {
		name      string
		overrides map[string]model.Price
		model     string
		usage     model.Usage
		want      int64
		wantOK    bool
	}{
		{
			name:   "shipped price with cache reads and writes",
			model:  "claude-opus-5-5",
			usage:  model.Usage{InputTokens: 2_000, OutputTokens: 1_000, CacheReadTokens: 100_000, CacheWriteTokens: 10_000},
			want:   2_000*4 + 1_000*20 + 100_000*0.20 + 10_000*5,
			wantOK: true,
		},
		{
			name:   "Haiku 5.5 with a short prompt uses the lower tier",
			model:  "claude-haiku-5-5",
			usage:  model.Usage{InputTokens: 60_000, CacheReadTokens: 40_000, OutputTokens: 10_000},
			want:   60_000*0.10 + 40_000*0.01 + 10_000*0.50,
			wantOK: true,
		},
		{
			name:   "Haiku 5.5 with a long prompt uses the higher tier",
			model:  "claude-haiku-5-5",
			usage:  model.Usage{InputTokens: 60_001, CacheReadTokens: 40_000, OutputTokens: 10_000},
			want:   (60_001*500_000 + 40_000*50_000 + 10_000*2_500_000) / usd,
			wantOK: true,
		},
		{
			name:      "an override wins over the shipped price",
			overrides: map[string]model.Price{"claude-opus-5-5": override},
			model:     "claude-opus-5-5",
			usage:     model.Usage{InputTokens: 1_000, OutputTokens: 1_000, CacheReadTokens: 10_000, CacheWriteTokens: 1_000},
			want:      1_000*1 + 1_000*2 + 10_000*0.10 + 1_000*3,
			wantOK:    true,
		},
		{
			name:      "an override replaces the tiers too",
			overrides: map[string]model.Price{"claude-haiku-5-5": override},
			model:     "claude-haiku-5-5",
			usage:     model.Usage{InputTokens: 200_000},
			want:      200_000 * 1,
			wantOK:    true,
		},
		{
			name:      "an override adds a model",
			overrides: map[string]model.Price{"my-model": override},
			model:     "my-model",
			usage:     model.Usage{OutputTokens: 1_000_000},
			want:      2 * usd,
			wantOK:    true,
		},
		{
			name:  "unknown model",
			model: "claude-unknown",
			usage: model.Usage{InputTokens: 1_000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := New(tt.overrides).Cost(tt.model, tt.usage)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewDoesNotChangeTheShippedTable(t *testing.T) {
	t.Parallel()

	New(map[string]model.Price{"claude-opus-5-5": {}, "my-model": {}})

	p, ok := Shipped().Lookup("claude-opus-5-5")
	require.True(t, ok)
	require.Equal(t, perMTok(4, 20, 0.20, 5), p)

	_, ok = Shipped().Lookup("my-model")
	require.False(t, ok)
}

func TestShippedPrices(t *testing.T) {
	t.Parallel()

	for _, id := range Shipped().IDs() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			p, ok := Shipped().Lookup(id)
			require.True(t, ok)
			requireSane(t, p)

			if p.Above != nil {
				require.Positive(t, p.TierAboveInputTokens)
				requireSane(t, *p.Above)
				require.Nil(t, p.Above.Above)
				require.Greater(t, p.Above.InputPerMTok, p.InputPerMTok)
			}
		})
	}
}

func TestTableUnpriced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		models map[string]string
		want   []Unpriced
	}{
		{name: "no connectors", models: nil, want: nil},
		{name: "every model priced", models: map[string]string{"claude": "claude-opus-5-5"}, want: nil},
		{
			name:   "unpriced models sorted by connector",
			models: map[string]string{"local": "qwen", "claude": "claude-opus-5-5", "cheap": "claude-unknown"},
			want:   []Unpriced{{Connector: "cheap", Model: "claude-unknown"}, {Connector: "local", Model: "qwen"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, Shipped().Unpriced(tt.models))
		})
	}
}

func TestDollars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   float64
		want int64
	}{
		{name: "whole dollars", in: 25, want: 25_000_000},
		{name: "an eighth of a dollar", in: 0.125, want: 125_000},
		{name: "one cent", in: 0.01, want: 10_000},
		{name: "a value floats cannot hold exactly", in: 0.1, want: 100_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, dollars(tt.in))
		})
	}
}

// requireSane checks that every field of a shipped price is set and that a
// cache read costs less than input, which costs less than a cache write.
func requireSane(t *testing.T, p model.Price) {
	t.Helper()

	require.Positive(t, p.CacheReadPerMTok)
	require.Less(t, p.CacheReadPerMTok, p.InputPerMTok)
	require.Less(t, p.InputPerMTok, p.CacheWritePerMTok)
	require.Less(t, p.InputPerMTok, p.OutputPerMTok)
}

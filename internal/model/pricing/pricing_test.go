package pricing

import (
	"testing"
	"time"

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
			name:   "OpenAI model with a short prompt",
			model:  "gpt-6-sol",
			usage:  model.Usage{InputTokens: 10_000, CacheReadTokens: 200_000, CacheWriteTokens: 62_000, OutputTokens: 5_000},
			want:   10_000*2 + 200_000*0.20 + 62_000*2.50 + 5_000*10,
			wantOK: true,
		},
		{
			name:   "OpenAI model with a prompt above 272K tokens uses the long-context price",
			model:  "gpt-6-sol",
			usage:  model.Usage{InputTokens: 10_000, CacheReadTokens: 200_000, CacheWriteTokens: 62_001, OutputTokens: 5_000},
			want:   10_000*4 + 200_000*0.40 + 62_001*5 + 5_000*15,
			wantOK: true,
		},
		{
			name:   "Foundry model from another provider",
			model:  "DeepSeek-V4-Pro",
			usage:  model.Usage{InputTokens: 10_000, CacheReadTokens: 90_000, OutputTokens: 2_000},
			want:   (10_000*1_740_000 + 90_000*145_000 + 2_000*3_480_000) / usd,
			wantOK: true,
		},
		{
			name:   "Gemini model with cached input",
			model:  "gemini-3.8-flash",
			usage:  model.Usage{InputTokens: 266, CacheReadTokens: 1_024, OutputTokens: 138},
			want:   (266*750_000 + 1_024*75_000 + 138*3_750_000) / usd,
			wantOK: true,
		},
		{
			name:   "Gemini model with a prompt above 200K tokens uses the long-context price",
			model:  "gemini-3.1-pro-preview",
			usage:  model.Usage{InputTokens: 150_000, CacheReadTokens: 50_001, OutputTokens: 2_000},
			want:   (150_000*4_000_000 + 50_001*400_000 + 2_000*18_000_000) / usd,
			wantOK: true,
		},
		{
			name:   "OpenAI embedding model",
			model:  "text-embedding-3-small",
			usage:  model.Usage{InputTokens: 500_000},
			want:   500_000 * 0.02,
			wantOK: true,
		},
		{
			name:   "Voyage embedding model",
			model:  "voyage-4",
			usage:  model.Usage{InputTokens: 2_000_000},
			want:   2_000_000 * 0.06,
			wantOK: true,
		},
		{
			name:   "Titan embedding model",
			model:  "amazon.titan-embed-text-v2:0",
			usage:  model.Usage{InputTokens: 1_000_000},
			want:   1_000_000 * 0.02,
			wantOK: true,
		},
		{
			name:   "Cohere embedding model through a geo inference profile",
			model:  "eu.cohere.embed-v4:0",
			usage:  model.Usage{InputTokens: 1_000_000},
			want:   1_000_000 * 0.12,
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

func TestTableLookupAt(t *testing.T) {
	t.Parallel()

	before := perMTok(2, 10, 0.10, 2)
	after := perMTok(4, 20, 0.20, 4)
	override := perMTok(3, 15, 0.30, 3)

	start := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	table := Table{prices: map[string][]period{
		"dated": {{price: before}, {from: start, price: after}},
		"plain": {{price: before}},
		"two":   {{price: before}, {from: start, price: after}, {from: start.AddDate(0, 1, 0), price: override}},
	}}

	tests := []struct {
		name   string
		model  string
		at     time.Time
		want   model.Price
		wantOK bool
	}{
		{name: "long before the start date", model: "dated", at: start.AddDate(-1, 0, 0), want: before, wantOK: true},
		{name: "the last nanosecond before the start date", model: "dated", at: start.Add(-time.Nanosecond), want: before, wantOK: true},
		{name: "on the start date", model: "dated", at: start, want: after, wantOK: true},
		{name: "after the start date", model: "dated", at: start.AddDate(1, 0, 0), want: after, wantOK: true},
		{name: "a later time zone before the start date in UTC", model: "dated", at: start.In(time.FixedZone("UTC+2", 2*60*60)).Add(-time.Hour), want: before, wantOK: true},
		{name: "a price with no start date", model: "plain", at: start.AddDate(5, 0, 0), want: before, wantOK: true},
		{name: "between two start dates", model: "two", at: start.AddDate(0, 0, 10), want: after, wantOK: true},
		{name: "after the second start date", model: "two", at: start.AddDate(0, 1, 0), want: override, wantOK: true},
		{name: "unknown model", model: "unknown", at: start},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := table.LookupAt(tt.model, tt.at)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTableCostAtAndNow(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	usage := model.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	table := Table{prices: map[string][]period{
		"dated": {{price: perMTok(2, 10, 0.10, 2)}, {from: start, price: perMTok(4, 20, 0.20, 4)}},
		"past":  {{price: perMTok(1, 2, 0.1, 1)}, {from: time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC), price: perMTok(3, 6, 0.3, 3)}},
		"later": {{price: perMTok(1, 2, 0.1, 1)}, {from: time.Now().AddDate(1, 0, 0), price: perMTok(3, 6, 0.3, 3)}},
	}}

	got, ok := table.CostAt("dated", usage, start.Add(-time.Second))
	require.True(t, ok)
	require.EqualValues(t, 12*usd, got)

	got, ok = table.CostAt("dated", usage, start)
	require.True(t, ok)
	require.EqualValues(t, 24*usd, got)

	_, ok = table.CostAt("unknown", usage, start)
	require.False(t, ok)

	got, ok = table.Cost("past", usage)
	require.True(t, ok)
	require.EqualValues(t, 9*usd, got, "Cost uses the price that applies now")

	p, ok := table.Lookup("later")
	require.True(t, ok)
	require.Equal(t, perMTok(1, 2, 0.1, 1), p, "Lookup ignores a price that starts later")
}

func TestBuild(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	today := perMTok(2, 10, 0.10, 2)
	rise := perMTok(4, 20, 0.20, 4)
	override := perMTok(1, 2, 0.1, 1)

	base := map[string]model.Price{"dated": today, "plain": today}
	changes := map[string][]period{"dated": {{from: start, price: rise}}}

	tests := []struct {
		name      string
		overrides map[string]model.Price
		model     string
		at        time.Time
		want      model.Price
		wantOK    bool
	}{
		{name: "the shipped price before the start date", model: "dated", at: start.Add(-time.Second), want: today, wantOK: true},
		{name: "the shipped price on the start date", model: "dated", at: start, want: rise, wantOK: true},
		{name: "the shipped price after the start date", model: "dated", at: start.AddDate(1, 0, 0), want: rise, wantOK: true},
		{name: "a model with no change", model: "plain", at: start.AddDate(1, 0, 0), want: today, wantOK: true},
		{
			name: "an override replaces the price before the start date", overrides: map[string]model.Price{"dated": override},
			model: "dated", at: start.Add(-time.Second), want: override, wantOK: true,
		},
		{
			name: "an override replaces the price on the start date", overrides: map[string]model.Price{"dated": override},
			model: "dated", at: start, want: override, wantOK: true,
		},
		{
			name: "an override replaces the price after the start date", overrides: map[string]model.Price{"dated": override},
			model: "dated", at: start.AddDate(1, 0, 0), want: override, wantOK: true,
		},
		{
			name: "an override adds a model", overrides: map[string]model.Price{"added": override},
			model: "added", at: start, want: override, wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := build(base, changes, tt.overrides).LookupAt(tt.model, tt.at)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestShippedChangesAreSane(t *testing.T) {
	t.Parallel()

	for id, changes := range shippedChanges {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			_, ok := shipped[id]
			require.True(t, ok, "a change needs a shipped price to follow")

			previous := time.Time{}
			for _, c := range changes {
				require.True(t, c.from.After(previous), "changes are sorted by date, none on the zero time")
				require.Equal(t, time.UTC, c.from.Location())
				requireSane(t, c.price)

				previous = c.from
			}
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

			if _, embedding := shippedEmbeddings[id]; embedding {
				requireSaneEmbedding(t, p)

				return
			}

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
// cache read costs less than input, which costs no more than a cache write.
// Some OpenAI models charge nothing extra for cache writes.
func requireSane(t *testing.T, p model.Price) {
	t.Helper()

	require.Positive(t, p.CacheReadPerMTok)
	require.Less(t, p.CacheReadPerMTok, p.InputPerMTok)
	require.LessOrEqual(t, p.InputPerMTok, p.CacheWritePerMTok)
	require.Less(t, p.InputPerMTok, p.OutputPerMTok)
}

// requireSaneEmbedding checks that an embedding price has an input price
// and nothing else, since an embedding call has only input tokens.
func requireSaneEmbedding(t *testing.T, p model.Price) {
	t.Helper()

	require.Positive(t, p.InputPerMTok)
	require.Zero(t, p.OutputPerMTok)
	require.Zero(t, p.CacheReadPerMTok)
	require.Zero(t, p.CacheWritePerMTok)
	require.Nil(t, p.Above)
}

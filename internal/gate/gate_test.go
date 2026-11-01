package gate

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

func TestCovers(t *testing.T) {
	t.Parallel()

	latency := strategy.Strategy{
		Name:  "latency-alert",
		Match: strategy.Match{AlertNames: []string{"*Latency*"}, TextPattern: "(?i)slow"},
	}

	generic := strategy.Strategy{Name: config.DefaultStrategy}

	tests := []struct {
		name  string
		named string
		st    strategy.Strategy
		ev    event.Event
		want  bool
	}{
		{name: "a scenario that names the strategy", named: "latency-alert", st: latency, ev: event.Event{AlertName: "Disk"}, want: true},
		{name: "a scenario that names another strategy is not matched", named: "service-down", st: latency, ev: event.Event{AlertName: "CheckoutLatencyHigh"}},
		{name: "a scenario on the default strategy whose alert name fits", named: config.DefaultStrategy, st: latency, ev: event.Event{AlertName: "CheckoutLatencyHigh"}, want: true},
		{name: "a scenario on the default strategy whose text fits", named: config.DefaultStrategy, st: latency, ev: event.Event{AlertName: "Disk", Title: "Checkout is slow"}, want: true},
		{name: "a scenario on the default strategy that does not fit", named: config.DefaultStrategy, st: latency, ev: event.Event{AlertName: "Disk", Text: "Disk is full"}},
		{name: "every scenario that names the default covers the default", named: config.DefaultStrategy, st: generic, ev: event.Event{AlertName: "Anything"}, want: true},
		{name: "a strategy with no match covers only the scenarios that name it", named: config.DefaultStrategy, st: strategy.Strategy{Name: "empty"}, ev: event.Event{AlertName: "Anything"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, covers(tt.named, tt.st, tt.ev))
		})
	}
}

func TestEventText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   event.Event
		want string
	}{
		{name: "title and text", ev: event.Event{Title: "Title", Text: "Text"}, want: "Title\nText"},
		{name: "title only", ev: event.Event{Title: "Title"}, want: "Title"},
		{name: "text only", ev: event.Event{Text: "Text"}, want: "Text"},
		{name: "neither", ev: event.Event{}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, eventText(tt.ev))
		})
	}
}

func TestSetHash(t *testing.T) {
	t.Parallel()

	scenario := func(id, verdict string, payload any) bench.Scenario {
		sc := bench.Scenario{ID: id}
		sc.Event.Payload = payload
		sc.Expected.Verdict = report.Verdict(verdict)

		return sc
	}

	base := []bench.Scenario{scenario("a", "true_positive", map[string]any{"x": 1}), scenario("b", "false_positive", map[string]any{"y": 2})}

	tests := []struct {
		name      string
		other     []bench.Scenario
		wantEqual bool
	}{
		{name: "the same scenarios", other: []bench.Scenario{scenario("a", "true_positive", map[string]any{"x": 1}), scenario("b", "false_positive", map[string]any{"y": 2})}, wantEqual: true},
		{name: "another scenario", other: []bench.Scenario{scenario("a", "true_positive", map[string]any{"x": 1}), scenario("c", "false_positive", map[string]any{"y": 2})}},
		{name: "another payload", other: []bench.Scenario{scenario("a", "true_positive", map[string]any{"x": 2}), scenario("b", "false_positive", map[string]any{"y": 2})}},
		{name: "another expected verdict", other: []bench.Scenario{scenario("a", "inconclusive", map[string]any{"x": 1}), scenario("b", "false_positive", map[string]any{"y": 2})}},
		{name: "a scenario fewer", other: base[:1]},
	}

	want, err := setHash(base)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := setHash(tt.other)
			require.NoError(t, err)
			require.Equal(t, tt.wantEqual, got == want)
		})
	}
}

func TestSeedOfAndNames(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("01920000-0000-7000-8000-0123456789ab")

	require.Equal(t, uint64(0x80000123456789ab), seedOf(id), "the seed is the last eight bytes of the ID")
	require.Equal(t, "sidekick_bench_gate_019200000000700080000123456789ab", schemaName(id))
	require.Equal(t, "proposal:01920000", proposalVersion(id))
}

func TestCapMicros(t *testing.T) {
	t.Parallel()

	tests := []struct {
		usd  float64
		want int64
	}{
		{usd: 5, want: 5_000_000},
		{usd: 0.000001, want: 1},
		{usd: 2.5, want: 2_500_000},
		{usd: 0.1 + 0.2, want: 300_000},
	}

	for _, tt := range tests {
		require.Equal(t, tt.want, capMicros(config.BenchGate{MaxCostUSD: tt.usd}))
	}
}

package investigator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestBudgetReached(t *testing.T) {
	t.Parallel()

	started := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		budget  Budget
		inv     db.Investigation
		elapsed time.Duration
		// dailyCapped says a daily cap is reached.
		dailyCapped bool
		want        string
		wantHard    bool
	}{
		{name: "nothing used", budget: Budget{MaxToolCalls: 40, MaxCostMicros: 1_000_000, MaxWallTime: 10 * time.Minute}},
		{name: "no limits", inv: db.Investigation{ToolCalls: 1000, CostMicros: 1 << 40}, elapsed: 24 * time.Hour},
		{
			name:   "cost just below the soft limit",
			budget: Budget{MaxCostMicros: 1_000_000},
			inv:    db.Investigation{CostMicros: 899_999},
		},
		{
			name:   "cost at the soft limit",
			budget: Budget{MaxCostMicros: 1_000_000},
			inv:    db.Investigation{CostMicros: 900_000},
			want:   budgetCost,
		},
		{
			name:     "cost used up",
			budget:   Budget{MaxCostMicros: 1_000_000},
			inv:      db.Investigation{CostMicros: 1_000_000},
			want:     budgetCost,
			wantHard: true,
		},
		{
			name:   "tool calls at the soft limit",
			budget: Budget{MaxToolCalls: 40},
			inv:    db.Investigation{ToolCalls: 36},
			want:   budgetToolCalls,
		},
		{
			name:   "tool calls used up still allow the final turn",
			budget: Budget{MaxToolCalls: 40},
			inv:    db.Investigation{ToolCalls: 40},
			want:   budgetToolCalls,
		},
		{
			name:    "a minute before a 10 minute wall time",
			budget:  Budget{MaxWallTime: 10 * time.Minute},
			elapsed: 9*time.Minute + time.Second,
			want:    budgetWallTime,
		},
		{
			name:    "just before the soft limit of a 10 minute wall time",
			budget:  Budget{MaxWallTime: 10 * time.Minute},
			elapsed: 9*time.Minute - time.Second,
		},
		{
			name:    "a tenth before a short wall time",
			budget:  Budget{MaxWallTime: 2 * time.Minute},
			elapsed: 108*time.Second + time.Millisecond,
			want:    budgetWallTime,
		},
		{
			name:        "a daily cap",
			budget:      Budget{MaxToolCalls: 40},
			dailyCapped: true,
			want:        budgetDailyCap,
		},
		{
			name:        "a used up cost before a daily cap",
			budget:      Budget{MaxCostMicros: 1_000_000},
			inv:         db.Investigation{CostMicros: 1_000_000},
			dailyCapped: true,
			want:        budgetCost,
			wantHard:    true,
		},
		{
			name:    "just before the soft limit of a short wall time",
			budget:  Budget{MaxWallTime: 2 * time.Minute},
			elapsed: 107 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hist, err := newHistory(nil)
			require.NoError(t, err)

			inv := tt.inv
			inv.StartedAt = &started

			r := &run{budget: tt.budget, inv: inv, hist: hist}

			got, hard := r.budgetReached(started.Add(tt.elapsed), tt.dailyCapped)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantHard, hard)
		})
	}
}

func TestUSD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		micros int64
		want   string
	}{
		{micros: 0, want: "$0.00"},
		{micros: 750_000, want: "$0.75"},
		{micros: 40_000_000, want: "$40.00"},
		{micros: 10_500, want: "$0.01"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, usd(tt.micros))
		})
	}
}

package proposals

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

func TestTriggerThatIsOffNeverLooks(t *testing.T) {
	t.Parallel()

	strategy, version := "latency-alert", "v1"

	tests := []struct {
		name    string
		trigger Trigger
		inv     db.Investigation
	}{
		{name: "the zero value", trigger: Trigger{}, inv: db.Investigation{Strategy: &strategy, StrategyVersion: &version}},
		{name: "enabled with no threshold", trigger: Trigger{Enabled: true}, inv: db.Investigation{Strategy: &strategy, StrategyVersion: &version}},
		{name: "an investigation without a strategy", trigger: Trigger{Enabled: true, Threshold: 3}, inv: db.Investigation{StrategyVersion: &version}},
		{name: "an investigation without a version", trigger: Trigger{Enabled: true, Threshold: 3}, inv: db.Investigation{Strategy: &strategy}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A nil database would panic if the trigger used it.
			added, err := tt.trigger.Check(t.Context(), nil, tt.inv)

			require.NoError(t, err)
			require.False(t, added)
		})
	}
}

func TestBaseVersion(t *testing.T) {
	t.Parallel()

	origin := func(s string) *string { return &s }

	tests := []struct {
		name    string
		p       db.StrategyProposal
		want    int
		wantErr bool
	}{
		{name: "a runtime version", p: db.StrategyProposal{BaseOrigin: origin("runtime"), BaseVersion: "runtime:7"}, want: 7},
		{name: "a built-in strategy has no runtime version yet", p: db.StrategyProposal{BaseOrigin: origin("built-in"), BaseVersion: "0123456789abcdef"}, want: 0},
		{name: "a runtime version that is not one", p: db.StrategyProposal{BaseOrigin: origin("runtime"), BaseVersion: "0123456789abcdef"}, wantErr: true},
		{name: "a runtime version of zero", p: db.StrategyProposal{BaseOrigin: origin("runtime"), BaseVersion: "runtime:0"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := baseVersion(tt.p)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrBadBase)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReasonOf(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("01920000-0000-7000-8000-000000000301")
	rationale := func(s string) *string { return &s }

	tests := []struct {
		name string
		p    db.StrategyProposal
		want string
	}{
		{name: "the proposal and its rationale", p: db.StrategyProposal{ID: id, Rationale: rationale(" Planned deploys. ")}, want: "Proposal " + id.String() + ": Planned deploys."},
		{name: "no rationale", p: db.StrategyProposal{ID: id}, want: "Proposal " + id.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, reasonOf(tt.p))
		})
	}

	t.Run("a long rationale is cut to what a save takes", func(t *testing.T) {
		t.Parallel()

		got := reasonOf(db.StrategyProposal{ID: id, Rationale: rationale(strings.Repeat("é", 2000))})

		require.Equal(t, strategystore.MaxReasonLength, utf8.RuneCountInString(got))
		require.True(t, strings.HasSuffix(got, "…"))
	})
}

func TestKeys(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse("01920000-0000-7000-8000-000000000301")

	require.Equal(t, "propose_strategy:"+id.String(), ProposeStrategyKey(id))
	require.Equal(t, "bench_gate:"+id.String(), BenchGateKey(id))
}

func TestReviewerID(t *testing.T) {
	t.Parallel()

	id := uuid.New()

	require.Equal(t, &id, reviewerID(strategystore.Actor{ID: id.String()}))
	require.Nil(t, reviewerID(strategystore.Actor{ID: "U01SLACK"}))
}

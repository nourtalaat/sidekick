package feedback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestNextConfirmation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	cause := "Deploy 4f2c1a"

	confirmedAs := func(verdict string) confirmation {
		return confirmation{verdict: &verdict, bySlackUser: new("U01ANA"), at: &earlier}
	}

	byBen := func(verdict string) confirmation {
		return confirmation{verdict: &verdict, cause: &cause, bySlackUser: new("U01BEN"), at: &now}
	}

	tests := []struct {
		name       string
		cur        confirmation
		rating     output.Rating
		want       confirmation
		wantChange Change
	}{
		{name: "Correct on an unconfirmed verdict", rating: output.RatingCorrect, want: byBen("true_positive"), wantChange: Confirmed},
		{name: "Correct on the confirmed verdict", cur: confirmedAs("true_positive"), rating: output.RatingCorrect, want: confirmedAs("true_positive"), wantChange: Unchanged},
		{name: "Correct on another verdict than the confirmed one", cur: confirmedAs("false_positive"), rating: output.RatingCorrect, want: byBen("true_positive"), wantChange: Revised},
		{name: "Wrong on an unconfirmed verdict", rating: output.RatingWrong, wantChange: Unchanged},
		{name: "Wrong on the confirmed verdict", cur: confirmedAs("true_positive"), rating: output.RatingWrong, wantChange: Withdrawn},
		{name: "Wrong on another verdict than the confirmed one", cur: confirmedAs("false_positive"), rating: output.RatingWrong, want: confirmedAs("false_positive"), wantChange: Unchanged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, change := nextConfirmation(tt.cur, tt.rating, "true_positive", &cause, "U01BEN", now)
			require.Equal(t, tt.wantChange, change)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestVerdictRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		run  int32
		want string
	}{
		{run: 1, want: "run:1/verdict"},
		{run: 12, want: "run:12/verdict"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, VerdictRef(tt.run))
		})
	}
}

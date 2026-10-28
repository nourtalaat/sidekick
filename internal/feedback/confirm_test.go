package feedback

import (
	"testing"
	"time"

	"github.com/google/uuid"
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

			got, change := nextConfirmation(tt.cur, tt.rating, "true_positive", &cause, actor{slackUser: new("U01BEN")}, now)
			require.Equal(t, tt.wantChange, change)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNextRealVerdict(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC)
	earlier := now.Add(-time.Hour)
	who := actor{userID: new(uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1"))}

	confirmed := func(verdict, cause string) confirmation {
		return confirmation{verdict: &verdict, cause: &cause, bySlackUser: new("U01ANA"), at: &earlier}
	}

	byWho := func(verdict string, cause *string) confirmation {
		return confirmation{verdict: &verdict, cause: cause, by: who.userID, at: &now}
	}

	tests := []struct {
		name       string
		cur        confirmation
		verdict    string
		cause      *string
		want       confirmation
		wantChange Change
	}{
		{name: "nothing confirmed yet", verdict: "false_positive", cause: new("Load test"), want: byWho("false_positive", new("Load test")), wantChange: Confirmed},
		{name: "a verdict without a cause", verdict: "false_positive", want: byWho("false_positive", nil), wantChange: Confirmed},
		{name: "another verdict than the confirmed one", cur: confirmed("true_positive", "Deploy"), verdict: "false_positive", cause: new("Load test"), want: byWho("false_positive", new("Load test")), wantChange: Revised},
		{name: "another verdict drops the old cause", cur: confirmed("true_positive", "Deploy"), verdict: "false_positive", want: byWho("false_positive", nil), wantChange: Revised},
		{name: "the confirmed verdict with no cause", cur: confirmed("true_positive", "Deploy"), verdict: "true_positive", want: confirmed("true_positive", "Deploy"), wantChange: Unchanged},
		{name: "the confirmed verdict with the same cause", cur: confirmed("true_positive", "Deploy"), verdict: "true_positive", cause: new("Deploy"), want: confirmed("true_positive", "Deploy"), wantChange: Unchanged},
		{name: "the confirmed verdict with another cause", cur: confirmed("true_positive", "Deploy"), verdict: "true_positive", cause: new("Config"), want: byWho("true_positive", new("Config")), wantChange: Revised},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, change := nextRealVerdict(tt.cur, tt.verdict, tt.cause, who, now)
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

func TestConfirmationView(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 26, 14, 10, 0, 0, time.UTC)

	tests := []struct {
		name string
		c    confirmation
		want any
	}{
		{name: "nothing confirmed", c: confirmation{}, want: nil},
		{
			name: "a confirmed verdict",
			c:    confirmation{verdict: new("true_positive"), cause: new("Deploy 4f2c1a"), bySlackUser: new("U01ANA"), at: &at},
			want: &confirmationView{Verdict: "true_positive", Cause: "Deploy 4f2c1a", BySlackUser: "U01ANA", At: "2026-10-26T14:10:00Z"},
		},
		{
			name: "a verdict without a cause",
			c:    confirmation{verdict: new("false_positive"), bySlackUser: new("U01BEN"), at: &at},
			want: &confirmationView{Verdict: "false_positive", BySlackUser: "U01BEN", At: "2026-10-26T14:10:00Z"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.c.view())
		})
	}
}

//go:build integration

package feedback

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

// click is one click on a verdict button by a Slack user.
type click struct {
	user   string
	rating output.Rating
	// stored and change are what the click should return.
	stored bool
	change Change
}

func TestRateVerdict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		clicks []click
		// confirmedBy is who confirmed the verdict at the end, empty when
		// nobody has.
		confirmedBy string
		// ratings are the stored ratings at the end, oldest change first.
		ratings []output.Feedback
	}{
		{
			name:        "Correct confirms the verdict",
			clicks:      []click{{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed}},
			confirmedBy: "U01ANA",
			ratings:     []output.Feedback{{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}}},
		},
		{
			name:    "Wrong does not confirm it",
			clicks:  []click{{user: "U01ANA", rating: output.RatingWrong, stored: true, change: Unchanged}},
			ratings: []output.Feedback{{Rating: output.RatingWrong, By: output.Person{SlackUserID: "U01ANA"}}},
		},
		{
			name: "a repeated click changes nothing",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
				{user: "U01ANA", rating: output.RatingCorrect, stored: false, change: Unchanged},
			},
			confirmedBy: "U01ANA",
			ratings:     []output.Feedback{{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}}},
		},
		{
			name: "a second Correct keeps the first confirmation",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
				{user: "U01BEN", rating: output.RatingCorrect, stored: true, change: Unchanged},
			},
			confirmedBy: "U01ANA",
			ratings: []output.Feedback{
				{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}},
				{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01BEN"}},
			},
		},
		{
			name: "a later Wrong takes the confirmation back",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
				{user: "U01BEN", rating: output.RatingWrong, stored: true, change: Withdrawn},
			},
			ratings: []output.Feedback{
				{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}},
				{Rating: output.RatingWrong, By: output.Person{SlackUserID: "U01BEN"}},
			},
		},
		{
			name: "changing a rating replaces it",
			clicks: []click{
				{user: "U01ANA", rating: output.RatingWrong, stored: true, change: Unchanged},
				{user: "U01ANA", rating: output.RatingCorrect, stored: true, change: Confirmed},
			},
			confirmedBy: "U01ANA",
			ratings:     []output.Feedback{{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			for i, c := range tt.clicks {
				res, err := rate(t, pool, VerdictRating{InvestigationID: finalInvestigation, Rating: c.rating, SlackUserID: c.user})
				require.NoError(t, err)
				require.Equal(t, Result{Stored: c.stored, Confirmation: c.change}, res, "click %d", i+1)
			}

			inv := investigation(t, pool, finalInvestigation)

			if tt.confirmedBy == "" {
				require.Nil(t, inv.ConfirmedVerdict)
				require.Nil(t, inv.ConfirmedCause)
				require.Nil(t, inv.ConfirmedBySlackUser)
				require.Nil(t, inv.ConfirmedAt)
			} else {
				require.Equal(t, "true_positive", deref(inv.ConfirmedVerdict))
				require.Equal(t, "Deploy 4f2c1a", deref(inv.ConfirmedCause), "the cause comes from the report")
				require.Equal(t, tt.confirmedBy, deref(inv.ConfirmedBySlackUser))
				require.NotNil(t, inv.ConfirmedAt)
			}

			entry := memoryEntry(t, pool, finalInvestigation)
			if tt.confirmedBy == "" {
				require.Nil(t, entry, "an unconfirmed investigation is not in memory")
			} else {
				require.Equal(t, &memoryRow{verdict: "true_positive", summary: "Errors after a deploy.", cause: "Deploy 4f2c1a"}, entry)
			}

			got, err := Verdicts(t.Context(), pool, inv)
			require.NoError(t, err)
			require.Equal(t, tt.ratings, got)
		})
	}
}

func TestRateVerdictRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rating  VerdictRating
		wantErr error
	}{
		{
			name:    "a running investigation",
			rating:  VerdictRating{InvestigationID: runningInvestigation, Rating: output.RatingCorrect, SlackUserID: "U01ANA"},
			wantErr: ErrNotRatable,
		},
		{
			name:    "an investigation that does not exist",
			rating:  VerdictRating{InvestigationID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ff"), Rating: output.RatingCorrect, SlackUserID: "U01ANA"},
			wantErr: ErrNotFound,
		},
		{
			name:    "an unknown rating",
			rating:  VerdictRating{InvestigationID: finalInvestigation, Rating: "maybe", SlackUserID: "U01ANA"},
			wantErr: errBadRating,
		},
		{
			name:    "no user",
			rating:  VerdictRating{InvestigationID: finalInvestigation, Rating: output.RatingCorrect},
			wantErr: errNoUser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			_, err := rate(t, pool, tt.rating)
			require.ErrorIs(t, err, tt.wantErr)

			got, err := Verdicts(t.Context(), pool, investigation(t, pool, finalInvestigation))
			require.NoError(t, err)
			require.Empty(t, got, "nothing is stored")
		})
	}
}

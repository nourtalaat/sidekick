//go:build integration

package proposals_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestFeedbackFiresTheTrigger rates verdicts wrong through both ways a rating
// is stored, a click in Slack and a submission, and checks that the third
// wrong rating of one strategy version adds the proposal in the same
// transaction as the rating.
func TestFeedbackFiresTheTrigger(t *testing.T) {
	t.Parallel()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	tests := []struct {
		name string
		// rate stores a wrong rating of the verdict of investigation inv.
		rate func(t *testing.T, tx pgx.Tx, inv int, n int) error
	}{
		{
			name: "a click on Wrong in Slack",
			rate: func(t *testing.T, tx pgx.Tx, inv, n int) error {
				t.Helper()

				_, err := feedback.RateVerdict(t.Context(), tx, feedback.VerdictRating{
					InvestigationID: invID(inv), Rating: output.RatingWrong, SlackUserID: fmt.Sprintf("U%02d", n), Proposals: trigger,
				})
				if err != nil {
					return fmt.Errorf("rate the verdict: %w", err)
				}

				return nil
			},
		},
		{
			name: "a submission from the web UI",
			rate: func(t *testing.T, tx pgx.Tx, inv, n int) error {
				t.Helper()

				_, err := feedback.Submit(t.Context(), tx, feedback.Submission{
					InvestigationID: invID(inv), Target: feedback.Target{Kind: feedback.TargetVerdict, Ref: feedback.VerdictRef(1)},
					Rating: output.RatingWrong, Reason: "It is a real incident", Author: feedback.Author{UserID: uuid.New()},
					Redactor: redactor, Proposals: trigger,
				})
				if err != nil {
					return fmt.Errorf("submit the feedback: %w", err)
				}

				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			storetest.LoadFixture(t, pool, "fixture/investigations.sql")

			for n, inv := range []int{1, 2, 3} {
				err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
					return tt.rate(t, tx, inv, n)
				})
				require.NoError(t, err)

				if n < 2 {
					require.Empty(t, proposalRows(t, pool), "after %d wrong ratings", n+1)
				}
			}

			rows := proposalRows(t, pool)
			require.Len(t, rows, 1, "the third wrong rating adds the proposal")
			require.Equal(t, int32(3), rows[0].TriggerCount)
			require.Equal(t, 1, jobCount(t, pool))
		})
	}
}

func TestFeedbackThatChangesNoCountDoesNotCheck(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	// Two wrong ratings, and a correct one: below the threshold, and the
	// correct one never looks.
	for n, inv := range []int{1, 2} {
		insertRating(t, pool, n, wrongVerdict(inv, 0))
	}

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		_, err := feedback.RateVerdict(t.Context(), tx, feedback.VerdictRating{
			InvestigationID: invID(3), Rating: output.RatingCorrect, SlackUserID: "U09", Proposals: trigger,
		})
		if err != nil {
			return fmt.Errorf("rate the verdict: %w", err)
		}

		return nil
	})
	require.NoError(t, err)
	require.Empty(t, proposalRows(t, pool))
}

//go:build integration

package publish

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
)

// TestPublishAfterTheEventsAreDeleted checks that a feedback click on an
// investigation whose events the retention job deleted still edits its
// message, instead of failing the publish job until it is dead.
func TestPublishAfterTheEventsAreDeleted(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	final := report.Report{Verdict: report.TruePositive, Confidence: report.High, Reason: "Errors after a deploy."}

	_, err := h.pool.Exec(t.Context(),
		"UPDATE investigations SET status = 'final', verdict = $1 WHERE id = $2", string(final.Verdict), webhookInvestigation)
	require.NoError(t, err)

	h.enqueue(t, Update{InvestigationID: webhookInvestigation, Phase: output.PhaseFinal, Seq: 1, Report: &final})
	require.NoError(t, h.run(t))

	_, err = h.pool.Exec(t.Context(),
		"DELETE FROM events WHERE group_id = (SELECT group_id FROM investigations WHERE id = $1)", webhookInvestigation)
	require.NoError(t, err)

	h.inTx(t, func(tx pgx.Tx) error {
		if _, err := feedback.RateVerdict(t.Context(), tx, feedback.VerdictRating{
			InvestigationID: webhookInvestigation, Rating: output.RatingCorrect, SlackUserID: "U01ANA",
		}); err != nil {
			return fmt.Errorf("rate the verdict: %w", err)
		}

		return Refresh(t.Context(), tx, webhookInvestigation)
	})

	require.NoError(t, h.run(t), "the publish succeeds without the event")

	edit := lastUpdate(t, h)
	require.Contains(t, contextTexts(t, edit), ":thumbsup: Marked correct by <@U01ANA>")
	require.Contains(t, string(edit.Attachments), "Errors after a deploy.", "the report comes from the publish state")

	for _, job := range h.jobs(t) {
		require.Equal(t, queue.StatusDone, job.Status)
	}
}

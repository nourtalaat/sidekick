//go:build integration

package publish

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

// TestFinalShowsResponders checks that the final message says who rated
// the verdict or stopped the run, as of the publish that shows it.
func TestFinalShowsResponders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report report.Report
		// act is what responders do once the final message is posted.
		act  func(t *testing.T, h *harness)
		want string
	}{
		{
			name:   "a rating",
			report: report.Report{Verdict: report.TruePositive, Confidence: report.High, Reason: "Errors after a deploy."},
			act: func(t *testing.T, h *harness) {
				t.Helper()

				h.inTx(t, func(tx pgx.Tx) error {
					if _, err := feedback.RateVerdict(t.Context(), tx, feedback.VerdictRating{
						InvestigationID: webhookInvestigation, Rating: output.RatingCorrect, SlackUserID: "U01ANA",
					}); err != nil {
						return err
					}

					return Refresh(t.Context(), tx, webhookInvestigation)
				})
			},
			want: ":thumbsup: Marked correct by <@U01ANA>",
		},
		{
			name: "a stop",
			report: report.Report{
				Verdict: report.Inconclusive, Confidence: report.Low, Reason: "Stopped.", StopReason: report.StopCancelled,
			},
			act: func(t *testing.T, h *harness) {
				t.Helper()

				_, err := h.pool.Exec(t.Context(),
					"UPDATE investigations SET cancel_requested_by_slack_user = 'U01STOP' WHERE id = $1", webhookInvestigation)
				require.NoError(t, err)

				h.refresh(t)
			},
			want: "Stopped by <@U01STOP>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.open(t, webhookInvestigation, slackSink)
			require.NoError(t, h.run(t))

			_, err := h.pool.Exec(t.Context(),
				"UPDATE investigations SET status = 'final', verdict = $1 WHERE id = $2", string(tt.report.Verdict), webhookInvestigation)
			require.NoError(t, err)

			h.enqueue(t, Update{InvestigationID: webhookInvestigation, Phase: output.PhaseFinal, Seq: 1, Report: &tt.report})
			require.NoError(t, h.run(t))
			require.NotContains(t, contextTexts(t, lastUpdate(t, h)), tt.want, "nobody has acted yet")

			tt.act(t, h)
			require.NoError(t, h.run(t))
			require.Contains(t, contextTexts(t, lastUpdate(t, h)), tt.want)
		})
	}
}

// lastUpdate returns the last chat.update call the fake Slack got.
func lastUpdate(t *testing.T, h *harness) slackCall {
	t.Helper()

	calls := h.slack.callsTo("chat.update")
	require.NotEmpty(t, calls)

	return calls[len(calls)-1]
}

// contextTexts returns the text of every context block in call, one per
// line.
func contextTexts(t *testing.T, call slackCall) string {
	t.Helper()

	var attachments []struct {
		Blocks []struct {
			Type     string          `json:"type"`
			Elements json.RawMessage `json:"elements"`
		} `json:"blocks"`
	}
	require.NoError(t, json.Unmarshal(call.Attachments, &attachments))

	var lines []string

	for _, a := range attachments {
		for _, b := range a.Blocks {
			if b.Type != "context" {
				continue
			}

			var elements []struct {
				Text string `json:"text"`
			}
			require.NoError(t, json.Unmarshal(b.Elements, &elements))

			for _, e := range elements {
				lines = append(lines, e.Text)
			}
		}
	}

	return strings.Join(lines, "\n")
}

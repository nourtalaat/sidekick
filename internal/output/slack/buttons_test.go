package slack

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

func TestButtons(t *testing.T) {
	t.Parallel()

	cancelled := &report.Report{
		Verdict:    report.Inconclusive,
		Confidence: report.Low,
		Reason:     "A responder stopped the investigation before it reached a verdict.",
		StopReason: report.StopCancelled,
		Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK}},
	}

	tests := []struct {
		name   string
		update func() output.Update
		// actions are the action IDs of the last block, nil when it is not a
		// row of buttons.
		actions []string
		// context is the text of the context block just before the
		// buttons, or of the last block when there are none.
		context string
	}{
		{
			name:    "queued",
			update:  func() output.Update { return testUpdate(output.PhaseQueued, nil) },
			actions: []string{ActionStop},
			context: "Received <!date^1793023320^{time}|14:02 UTC> · Updates as the investigation runs",
		},
		{
			name:    "running",
			update:  func() output.Update { return runningUpdate(1) },
			actions: []string{ActionStop},
			context: "Received <!date^1793023320^{time}|14:02 UTC> · 3 events · Updates as the investigation runs",
		},
		{
			name: "final with feedback",
			update: func() output.Update {
				u := testUpdate(output.PhaseFinal, verdictReport(report.TruePositive))
				u.Feedback = []output.Feedback{
					{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "U01ANA"}},
					{Rating: output.RatingWrong, By: output.Person{SlackUserID: "U01CY"}},
					{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "W01BEN"}},
					{Rating: output.RatingCorrect, By: output.Person{SlackUserID: "<!channel>"}},
				}

				return u
			},
			actions: []string{ActionVerdictCorrect, ActionVerdictWrong, ActionFeedbackDetails},
			context: ":thumbsup: Marked correct by <@U01ANA>, <@W01BEN> · :thumbsdown: Marked wrong by <@U01CY>",
		},
		{
			name:    "triage only",
			update:  func() output.Update { return testUpdate(output.PhaseFinal, triageOnly()) },
			actions: []string{ActionVerdictCorrect, ActionVerdictWrong, ActionFeedbackDetails, ActionInvestigateAnyway},
			context: "Triage only · No full investigation ran",
		},
		{
			name:    "stopped by a responder",
			update:  func() output.Update { return stoppedUpdate(cancelled, &output.Person{SlackUserID: "U01STOP"}) },
			context: "Checked signoz · Stopped by <@U01STOP>",
		},
		{
			name:    "stopped by someone not in Slack",
			update:  func() output.Update { return stoppedUpdate(cancelled, nil) },
			context: "Checked signoz · Stopped by a responder",
		},
		{
			name: "could not finish",
			update: func() output.Update {
				return testUpdate(output.PhaseFinal, &report.Report{StopReason: report.StopError, Checked: cancelled.Checked})
			},
			context: "Checked signoz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := tt.update()
			blocks := mainView(u).attachment.Blocks
			last := blocks[len(blocks)-1]

			if tt.actions == nil {
				require.NotEqual(t, blockActions, last.Type, "no buttons")
				require.Equal(t, tt.context, last.Elements[0].Text)

				return
			}

			require.Equal(t, blockActions, last.Type)

			ids := make([]string, 0, len(last.Buttons))
			for _, b := range last.Buttons {
				ids = append(ids, b.ActionID)
				require.Equal(t, u.InvestigationID, b.Value, "each button carries the investigation ID")
			}

			require.Equal(t, tt.actions, ids)
			require.Equal(t, tt.context, blocks[len(blocks)-2].Elements[0].Text)
		})
	}
}

// TestBlockJSON checks that a block decodes to what it was encoded from,
// for each kind of block.
func TestBlockJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		block block
	}{
		{name: "section", block: section("*Findings*")},
		{name: "context", block: contextBlock("Checked signoz")},
		{name: "actions", block: stopButtons(testUpdate(output.PhaseRunning, nil))},
		{name: "section with fields", block: block{Type: blockSection, Fields: []textObject{mrkdwn("*Impact*\nSome")}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(tt.block)
			require.NoError(t, err)

			var got block
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, tt.block, got)
		})
	}
}

// stoppedUpdate returns the final update of a run that by stopped.
func stoppedUpdate(r *report.Report, by *output.Person) output.Update {
	u := testUpdate(output.PhaseFinal, r)
	u.StoppedBy = by

	return u
}

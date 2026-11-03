package slack

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

// rollbackProposal is a rollback that waits for a decision, from revision 7
// to revision 6.
func rollbackProposal(t *testing.T, running, goesBackTo action.Revision) action.Proposal {
	t.Helper()

	snap, err := action.RevisionSnapshot("kubernetes:deployment/shop/checkout", "7", running, goesBackTo)
	require.NoError(t, err)

	revisions, err := snap.Revisions()
	require.NoError(t, err)

	changes, err := action.Changes(snap)
	require.NoError(t, err)

	shown := revisions.Display()

	p := waitingProposal()
	p.Action = "checkout-rollback"
	p.Snapshot, p.Changes, p.Revisions = &snap, changes, &shown

	return p
}

func revisionsAt(id string) action.Revision {
	return action.Revision{Kind: "ReplicaSet revision", ID: id, Version: "checkout:1.4." + id}
}

func TestARollbackShowsBothRevisionsInPlaceOfTheFields(t *testing.T) {
	t.Parallel()

	deployed := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	running := revisionsAt("7")
	running.DeployedAt, running.DeployedBy, running.Notes = &deployed, "ana", "app=checkout:1.4.7"

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{rollbackProposal(t, running, revisionsAt("6"))}

	got := goldenJSON(t, mainView(u))

	require.Contains(t, got, "Rolls back from checkout:1.4.7 to checkout:1.4.6")
	require.Contains(t, got, `Running now\n  revision:    ReplicaSet revision 7\n  version:     checkout:1.4.7\n  deployed at: 2026-11-03 09:30 UTC\n  deployed by: ana\n  notes:       app=checkout:1.4.7`)
	require.Contains(t, got, `Goes back to\n  revision:    ReplicaSet revision 6\n  version:     checkout:1.4.6\n  deployed at: unknown\n  deployed by: unknown`)
	require.NotContains(t, got, "Changes, field by field", "the revisions are shown instead of the fields")

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.Contains(t, buttons[ActionApproveProposal].Confirm.Text.Text, "rolls back from checkout:1.4.7 to checkout:1.4.6")
	require.Contains(t, buttons, ActionRejectProposal)
}

func TestRevisionNamesFallBackToTheKindAndID(t *testing.T) {
	t.Parallel()

	running := action.Revision{Kind: "Helm release revision", ID: "12"}

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{rollbackProposal(t, running, action.Revision{Kind: "Helm release revision", ID: "11"})}

	got := goldenJSON(t, mainView(u))
	require.Contains(t, got, "Rolls back from Helm release revision 12 to Helm release revision 11")
	require.Contains(t, got, `version:     unknown`)
}

func TestRevisionValuesCannotAddMarkup(t *testing.T) {
	t.Parallel()

	running := revisionsAt("7")
	running.Version = "<!channel>"
	running.DeployedBy = "<https://evil.example.com|click>"
	running.Notes = "a`b\nc" + string(rune(0x202e))

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{rollbackProposal(t, running, revisionsAt("6"))}

	got := goldenJSON(t, mainView(u))

	for _, markup := range []string{"<!channel>", "<https://evil.example.com|click>"} {
		require.NotContains(t, got, markup)
	}

	require.Contains(t, got, "&lt;!channel&gt;")
	require.Contains(t, got, `a\\u0060b\\nc\\u202e`, "a backtick, a newline and a hidden character are spelled out")
}

func TestRevisionsThatDoNotFitGiveNoApproveButton(t *testing.T) {
	t.Parallel()

	running := revisionsAt("7")
	running.Notes = strings.Repeat("a", maxParamsChars)

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{rollbackProposal(t, running, revisionsAt("6"))}

	require.Contains(t, goldenJSON(t, mainView(u)), "The revisions are too long to show here")

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.NotContains(t, buttons, ActionApproveProposal, "an approver must not decide on text that was cut")
	require.Contains(t, buttons, ActionRejectProposal)
}

func TestALongRevisionIsShownWholeAndItsNameIsCut(t *testing.T) {
	t.Parallel()

	tag := strings.Repeat("v", 200)
	running := revisionsAt("7")
	running.Version = tag

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{rollbackProposal(t, running, revisionsAt("6"))}

	got := goldenJSON(t, mainView(u))
	require.Contains(t, got, "version:     "+tag, "the block holds the whole tag")
	require.NotContains(t, got, "Rolls back from "+tag)
	require.Contains(t, got, "Rolls back from "+strings.Repeat("v", maxRevisionNameChars-1)+ellipsis)
	require.Contains(t, buttonsOf(mainView(u).attachment.Blocks), ActionApproveProposal)
}

func TestASucceededRollbackShowsItsRevisionsAndAnUndoButton(t *testing.T) {
	t.Parallel()

	p := rollbackProposal(t, revisionsAt("7"), revisionsAt("6"))
	p.Status = action.StatusSucceeded
	p.DecidedByName = "Ana"
	p.Result = &action.Result{Summary: "Rolled back to revision 6."}
	p.Undoable = true

	u := testUpdate(output.PhaseFinal, verdictReport(report.TruePositive))
	u.Proposals = []action.Proposal{p}

	got := goldenJSON(t, mainView(u))
	require.Contains(t, got, "Rolled back from checkout:1.4.7 to checkout:1.4.6")
	require.NotContains(t, got, "What it changed")
	require.Contains(t, buttonsOf(mainView(u).attachment.Blocks), ActionUndoProposal)
}

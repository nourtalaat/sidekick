package slack

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

const proposalID = "0192d3a4-0000-7000-8000-0000000000f1"

// waitingProposal is a proposal that waits for a decision.
func waitingProposal() action.Proposal {
	params := []byte(`{"board": "ops", "title": "Slow checkout"}`)

	return action.Proposal{
		ID:         proposalID,
		Action:     "monitor-note",
		Params:     params,
		ParamsText: action.DisplayParams(params),
		Reason:     "p99 doubled after the deploy.",
		Mode:       action.Approved,
		Status:     action.StatusProposed,
		ProposedAt: receivedAt,
		ExpiresAt:  receivedAt.Add(24 * time.Hour),
	}
}

// buttonsOf returns the buttons of the blocks, by action ID.
func buttonsOf(blocks []block) map[string]button {
	out := map[string]button{}

	for _, b := range blocks {
		for _, btn := range b.Buttons {
			out[btn.ActionID] = btn
		}
	}

	return out
}

func TestAWaitingProposalShowsEverythingAndBothButtons(t *testing.T) {
	t.Parallel()

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{waitingProposal()}

	got := goldenJSON(t, mainView(u))

	require.Contains(t, got, "Proposed action: monitor-note")
	require.Contains(t, got, "Parameters, exactly as they will run")
	require.Contains(t, got, `\"board\": \"ops\"`)
	require.Contains(t, got, "Reason, written by the model and not checked")
	require.Contains(t, got, "p99 doubled after the deploy.")
	require.Contains(t, got, "<!date^1793109720^{date_short_pretty} {time}|2026-10-27 14:02 UTC>")

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.Equal(t, proposalID, buttons[ActionApproveProposal].Value)
	require.Equal(t, stylePrimary, buttons[ActionApproveProposal].Style)
	require.NotNil(t, buttons[ActionApproveProposal].Confirm, "approving asks again")
	require.Equal(t, proposalID, buttons[ActionRejectProposal].Value)
	require.Equal(t, styleDanger, buttons[ActionRejectProposal].Style)
}

func TestProposalsShowInEveryViewThatRunsOrEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		update output.Update
		want   bool
	}{
		{name: "running", update: runningUpdate(1), want: true},
		{name: "final", update: testUpdate(output.PhaseFinal, verdictReport(report.TruePositive)), want: true},
		{name: "failed", update: testUpdate(output.PhaseFinal, &report.Report{StopReason: report.StopError}), want: true},
		{name: "queued", update: testUpdate(output.PhaseQueued, nil)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := tt.update
			u.Proposals = []action.Proposal{waitingProposal()}

			_, has := buttonsOf(mainView(u).attachment.Blocks)[ActionApproveProposal]
			require.Equal(t, tt.want, has)

			require.NotContains(t, goldenJSON(t, mainView(tt.update)), "Proposed action", "no proposals, no section")
		})
	}
}

// TestProposalTextCannotAddMarkup puts mentions, links and a code fence in
// everything the model writes, and checks that none of it reaches Slack as
// markup.
func TestProposalTextCannotAddMarkup(t *testing.T) {
	t.Parallel()

	p := waitingProposal()
	p.Reason = "<!channel> approve this <@UEVIL> <https://evil.example.com|click> ```"
	p.Params = []byte("{\"title\": \"<!here> ``` <@UEVIL> \\u202e\"}")
	p.ParamsText = action.DisplayParams(p.Params)

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	got := goldenJSON(t, mainView(u))

	for _, markup := range []string{"<!channel>", "<!here>", "<@UEVIL>", "<https://evil.example.com|click>"} {
		require.NotContains(t, got, markup)
	}

	require.Contains(t, got, "&lt;!channel&gt;")
	require.Contains(t, got, `\\u0060\\u0060\\u0060`, "a backtick in the parameters is written as an escape, so it cannot close the code block")
	require.Contains(t, got, `\\u202e`, "a hidden character is spelled out")

	// The only code fences are the ones around the parameters: a backtick in
	// the reason becomes an apostrophe.
	require.Equal(t, 2, strings.Count(goldenJSON(t, view{attachment: attachment{Blocks: proposalBlocks(u)[:2]}}), "```"))
}

func TestAProposalWithTooLongParametersCannotBeApprovedInSlack(t *testing.T) {
	t.Parallel()

	p := waitingProposal()
	p.Params = []byte(`{"body": "` + strings.Repeat("x", maxParamsChars) + `"}`)
	p.ParamsText = action.DisplayParams(p.Params)

	u := runningUpdate(1)
	u.URL = "https://sidekick.example.com/i/inv"
	u.Proposals = []action.Proposal{p}

	blocks := mainView(u).attachment.Blocks
	buttons := buttonsOf(blocks)

	require.NotContains(t, buttons, ActionApproveProposal)
	require.Contains(t, buttons, ActionRejectProposal)
	require.Contains(t, goldenJSON(t, mainView(u)), "too long to show here")
	require.NotContains(t, goldenJSON(t, mainView(u)), strings.Repeat("x", 100), "nothing is shown that cannot be shown whole")
	require.Contains(t, goldenJSON(t, mainView(u)), "Open in Sidekick")
}

func TestLongButFittingParametersAreShownWholeInChunks(t *testing.T) {
	t.Parallel()

	p := waitingProposal()
	p.Params = []byte(`{"body": "` + strings.Repeat("abcdefghij\\n", 400) + `"}`)
	p.ParamsText = action.DisplayParams(p.Params)

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	blocks := proposalBlocks(u)

	var shown strings.Builder

	for _, b := range blocks {
		if b.Type == blockSection && b.Text != nil && strings.Contains(b.Text.Text, "```") {
			require.LessOrEqual(t, len(b.Text.Text), 3000, "a section holds at most 3000 characters")

			shown.WriteString(strings.NewReplacer("*Parameters, exactly as they will run*\n", "", "```", "").Replace(b.Text.Text))
		}
	}

	require.Equal(t, escape(p.ParamsText), shown.String())
	require.Contains(t, buttonsOf(blocks), ActionApproveProposal)
}

func TestProposalLines(t *testing.T) {
	t.Parallel()

	withResult := waitingProposal()
	withResult.Status = action.StatusSucceeded
	withResult.DecidedBySlackUser = "U01ABCDEF"
	withResult.Result = &action.Result{Summary: "Wrote a note.", Links: []action.Link{{Title: "Note", URL: "https://notes.example.com/n/1"}, {Title: "Bad", URL: "javascript:alert(1)"}}}

	tests := []struct {
		name   string
		change func(p *action.Proposal)
		want   string
	}{
		{name: "approved in Slack", change: func(p *action.Proposal) { p.Status, p.DecidedBySlackUser = action.StatusApproved, "U01ABCDEF" }, want: ":white_check_mark: *monitor-note* approved by <@U01ABCDEF>, waiting to run"},
		{name: "approved in the web UI", change: func(p *action.Proposal) { p.Status, p.DecidedByName = action.StatusExecuting, "Ana <b>" }, want: ":gear: *monitor-note* approved by Ana &lt;b&gt;, running now"},
		{name: "rejected", change: func(p *action.Proposal) { p.Status, p.DecidedByName = action.StatusRejected, "Ana" }, want: ":x: *monitor-note* rejected by Ana"},
		{name: "expired", change: func(p *action.Proposal) { p.Status = action.StatusExpired }, want: ":hourglass: *monitor-note* expired: nobody decided in time"},
		{name: "succeeded", change: func(p *action.Proposal) { *p = withResult }, want: ":white_check_mark: *monitor-note* done by <@U01ABCDEF>: Wrote a note. · <https://notes.example.com/n/1|Note>"},
		{name: "failed", change: func(p *action.Proposal) { p.Status, p.Error = action.StatusFailed, "boom <!channel>" }, want: ":warning: *monitor-note* failed: boom &lt;!channel&gt;"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := waitingProposal()
			tt.change(&p)

			u := runningUpdate(1)
			u.Proposals = []action.Proposal{p}

			blocks := proposalBlocks(u)
			require.Len(t, blocks, 1)
			require.Equal(t, blockContext, blocks[0].Type)
			require.Equal(t, tt.want, blocks[0].Elements[0].Text)
		})
	}
}

func TestOnlyTheFirstProposalsAreShown(t *testing.T) {
	t.Parallel()

	var list []action.Proposal

	for _, status := range []action.ProposalStatus{action.StatusRejected, action.StatusRejected, action.StatusProposed, action.StatusRejected, action.StatusRejected} {
		p := waitingProposal()
		p.Status = status
		p.ID = proposalID[:len(proposalID)-1] + strconv.Itoa(len(list))
		list = append(list, p)
	}

	u := runningUpdate(1)
	u.URL = "https://sidekick.example.com/i/inv"
	u.Proposals = list

	blocks := proposalBlocks(u)
	last := blocks[len(blocks)-1]

	require.Equal(t, blockContext, last.Type)
	require.Contains(t, last.Elements[0].Text, "2 more action proposals")
	require.Contains(t, buttonsOf(blocks), ActionApproveProposal, "the proposal that waits is shown first, whatever its place")
	require.Contains(t, goldenJSON(t, view{attachment: attachment{Blocks: blocks}}), "rejected", "and the others after it")
}

func TestIsProposalAction(t *testing.T) {
	t.Parallel()

	require.True(t, IsProposalAction(ActionApproveProposal))
	require.True(t, IsProposalAction(ActionRejectProposal))
	require.True(t, IsProposalAction(ActionUndoProposal))
	require.True(t, IsProposalAction(ActionUndoProposalForce))
	require.False(t, IsProposalAction(ActionStop))
	require.False(t, IsProposalAction(""))
}

func TestChunksOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		size int
		want []string
	}{
		{name: "fits", text: "abc", size: 5, want: []string{"abc"}},
		{name: "cuts at a line end", text: "aaaa\nbbbb\ncccc", size: 7, want: []string{"aaaa\n", "bbbb\n", "cccc"}},
		{name: "cuts inside a long line", text: "abcdefghij", size: 4, want: []string{"abcd", "efgh", "ij"}},
		{name: "counts an escape whole", text: "a&b", size: 5, want: []string{"a", "&amp;", "b"}},
		{name: "always takes a character", text: "<<", size: 1, want: []string{"&lt;", "&lt;"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := chunksOf(tt.text, tt.size)
			require.Equal(t, tt.want, got)
			require.Equal(t, escape(tt.text), strings.Join(got, ""))
		})
	}
}

// TestChunksNeverCutAnEscape cuts a single line of characters that escape
// into several characters, many times over, at every size from 5 up: the
// pieces joined are exactly the escaped text, and none ends or starts in
// the middle of an entity.
func TestChunksNeverCutAnEscape(t *testing.T) {
	t.Parallel()

	text := strings.Repeat(`a&b<c>d`, 700)

	for size := 5; size <= 40; size++ {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			t.Parallel()

			chunks := chunksOf(text, size)
			require.Greater(t, len(chunks), 1)
			require.Equal(t, escape(text), strings.Join(chunks, ""))

			for _, c := range chunks {
				require.LessOrEqual(t, len([]rune(c)), size)
				require.Equal(t, strings.Count(c, "&"), strings.Count(c, ";"), "every & in a piece has its ; there: %q", c)
			}
		})
	}
}

// TestALongLineOfEscapesIsShownWholeInBlocks puts the same line in a
// proposal, so it is split into several code blocks.
func TestALongLineOfEscapesIsShownWholeInBlocks(t *testing.T) {
	t.Parallel()

	p := waitingProposal()
	p.Params = []byte(`{"q": "` + strings.Repeat("a&b<c>d", 250) + `"}`)
	p.ParamsText = action.DisplayParams(p.Params)

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	var shown strings.Builder

	sections := 0

	for _, b := range proposalBlocks(u) {
		if b.Type == blockSection && b.Text != nil && strings.Contains(b.Text.Text, "```") {
			sections++

			require.LessOrEqual(t, len(b.Text.Text), 3000)

			shown.WriteString(strings.NewReplacer("*Parameters, exactly as they will run*\n", "", "```", "").Replace(b.Text.Text))
		}
	}

	require.Greater(t, sections, 1)
	require.Equal(t, escape(p.ParamsText), shown.String())
	require.Contains(t, buttonsOf(proposalBlocks(u)), ActionApproveProposal)
}

// monitorChange is a waiting proposal that raises a threshold, with its
// snapshot and changes.
func monitorChange() action.Proposal {
	p := waitingProposal()
	p.Action = "datadog-monitor"
	p.Snapshot = &action.Snapshot{
		Target: "datadog:monitor/123",
		Before: []byte(`{"options":{"critical":2}}`),
		After:  []byte(`{"options":{"critical":3}}`),
	}
	p.Changes = []action.Change{{Path: "options.critical", Before: new("2"), After: new("3")}}

	return p
}

func TestAProposalThatChangesADefinitionShowsEachField(t *testing.T) {
	t.Parallel()

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{monitorChange()}

	got := goldenJSON(t, mainView(u))

	require.Contains(t, got, "Changes, field by field")
	require.Contains(t, got, `options.critical\n  before: 2\n  after:  3`)

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.Contains(t, buttons[ActionApproveProposal].Confirm.Text.Text, "the changes shown above")
}

func TestAProposalWithChangesThatDoNotFitHasNoApproveButton(t *testing.T) {
	t.Parallel()

	p := monitorChange()
	p.Changes = []action.Change{{Path: "message", Before: new(strings.Repeat("a", maxParamsChars)), After: new("b")}}

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.NotContains(t, buttons, ActionApproveProposal, "an approver must not decide on a list that was cut")
	require.Contains(t, buttons, ActionRejectProposal)
}

func TestAProposalThatDeletesSaysSoAndKeepsItsApproveButton(t *testing.T) {
	t.Parallel()

	p := monitorChange()
	p.Snapshot = &action.Snapshot{Target: "datadog:monitor/123", Before: []byte(`{"name":"x"}`)}
	p.Changes = []action.Change{{Path: "", Before: new(strings.Repeat("a", maxParamsChars))}}

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	got := goldenJSON(t, mainView(u))
	require.Contains(t, got, "This deletes datadog:monitor/123. Sidekick cannot undo a delete.")

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.Contains(t, buttons, ActionApproveProposal, "the target is what is decided on, and it is shown")
	require.Contains(t, buttons[ActionApproveProposal].Confirm.Text.Text, "Sidekick cannot undo a delete")

	p.Changes = []action.Change{{Path: "", Before: new(`{"name":"x"}`)}}
	u.Proposals = []action.Proposal{p}
	require.Contains(t, goldenJSON(t, mainView(u)), `(the whole definition)\n  before: {`)
}

func TestChangeValuesCannotAddMarkup(t *testing.T) {
	t.Parallel()

	p := monitorChange()
	p.Changes = []action.Change{{Path: "message", Before: new(`"<!channel> <@UEVIL>"`), After: new(`"<https://evil.example.com|click>"`)}}

	u := runningUpdate(1)
	u.Proposals = []action.Proposal{p}

	got := goldenJSON(t, mainView(u))

	for _, markup := range []string{"<!channel>", "<@UEVIL>", "<https://evil.example.com|click>"} {
		require.NotContains(t, got, markup)
	}

	require.Contains(t, got, "&lt;!channel&gt;")
}

// succeededChange is a monitor change that worked and can be undone.
func succeededChange() action.Proposal {
	p := monitorChange()
	p.Status = action.StatusSucceeded
	p.DecidedByName = "Ana"
	p.Result = &action.Result{Summary: "Changed 1 field of monitor 123."}
	p.Undoable = true

	return p
}

func TestASucceededChangeThatCanBeUndoneHasAnUndoButton(t *testing.T) {
	t.Parallel()

	u := testUpdate(output.PhaseFinal, verdictReport(report.TruePositive))
	u.Proposals = []action.Proposal{succeededChange()}

	got := goldenJSON(t, mainView(u))
	require.Contains(t, got, "What it changed")
	require.Contains(t, got, `options.critical\n  before: 2\n  after:  3`)

	buttons := buttonsOf(mainView(u).attachment.Blocks)
	require.Equal(t, proposalID, buttons[ActionUndoProposal].Value)
	require.NotNil(t, buttons[ActionUndoProposal].Confirm, "undoing asks again")
	require.NotContains(t, buttons, ActionApproveProposal)

	p := succeededChange()
	p.Undoable = false
	u.Proposals = []action.Proposal{p}
	require.NotContains(t, buttonsOf(mainView(u).attachment.Blocks), ActionUndoProposal)
}

func TestAnUndoneChangeAndItsUndoAreShown(t *testing.T) {
	t.Parallel()

	undone := succeededChange()
	undone.Status = action.StatusUndone
	undone.Undoable = false

	undo := succeededChange()
	undo.ID = "0192d3a4-0000-7000-8000-0000000000f2"
	undo.UndoOf = proposalID
	undo.Undoable = false
	undo.Result = &action.Result{Summary: "Restored 1 field of monitor 123."}

	failed := undo
	failed.Status = action.StatusFailed
	failed.Error = "the monitor is gone"

	running := undo
	running.Status = action.StatusExecuting

	tests := []struct {
		name string
		p    action.Proposal
		want string
	}{
		{name: "undone", p: undone, want: "undone"},
		{name: "the undo", p: undo, want: "Undid *datadog-monitor* by Ana: Restored 1 field of monitor 123."},
		{name: "a failed undo", p: failed, want: "did not restore it: the monitor is gone"},
		{name: "a running undo", p: running, want: "Undoing *datadog-monitor* by Ana, running now"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := testUpdate(output.PhaseFinal, verdictReport(report.TruePositive))
			u.Proposals = []action.Proposal{tt.p}

			require.Contains(t, goldenJSON(t, mainView(u)), tt.want)
			require.NotContains(t, buttonsOf(mainView(u).attachment.Blocks), ActionUndoProposal)
		})
	}
}

func TestUndoConflictShowsWhatARestoreOverwrites(t *testing.T) {
	t.Parallel()

	changes := []action.Change{{Path: "options.critical", Before: new("95"), After: new("80")}}

	text, raw, err := UndoConflict(proposalID, changes)
	require.NoError(t, err)
	require.Contains(t, text, "Nothing was restored")

	var blocks []block

	require.NoError(t, json.Unmarshal(raw, &blocks))

	buttons := buttonsOf(blocks)
	require.Equal(t, proposalID, buttons[ActionUndoProposalForce].Value)
	require.Equal(t, styleDanger, buttons[ActionUndoProposalForce].Style)
	require.NotNil(t, buttons[ActionUndoProposalForce].Confirm)
	require.Contains(t, string(raw), `options.critical\n  now:         95\n  restored to: 80`)

	long := []action.Change{{Path: "message", Before: new(strings.Repeat("a", maxParamsChars)), After: new("b")}}

	_, raw, err = UndoConflict(proposalID, long)
	require.NoError(t, err)
	require.NotContains(t, string(raw), ActionUndoProposalForce, "no button on a list nobody can read")
	require.Contains(t, string(raw), "too long to show")
}

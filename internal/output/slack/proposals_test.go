package slack

import (
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := chunksOf(tt.text, tt.size)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.text, strings.Join(got, ""))
		})
	}
}

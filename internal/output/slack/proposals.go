package slack

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
)

// The action IDs of the buttons of an action proposal. Each button's value
// is the proposal ID; the Slack app reads both back from a click.
const (
	ActionApproveProposal = "approve_proposal"
	ActionRejectProposal  = "reject_proposal"
)

// blockIDProposal starts the block ID of each proposal's buttons, followed
// by the proposal ID.
const blockIDProposal = "proposal"

// What the message shows about proposals, at most. A proposal whose
// parameters do not fit gets no Approve button: an approver must never
// decide on text that was cut.
const (
	maxProposalsShown = 3
	// maxParamsChars is the longest parameter text shown, in characters of
	// the escaped text, and maxParamsChunk the most one code block holds.
	maxParamsChars = 5400
	maxParamsChunk = 2700
	maxResultChars = 300
)

// IsProposalAction reports whether actionID is the action ID of the
// Approve or Reject button of a proposal.
func IsProposalAction(actionID string) bool {
	return actionID == ActionApproveProposal || actionID == ActionRejectProposal
}

// proposalBlocks renders the action proposals of u: the ones that wait for
// a decision first, each with its parameters in full, the model's reason
// and the Approve and Reject buttons, then a line for each of the others.
// It shows at most maxProposalsShown and says how many more there are in
// Sidekick. The parameters, the reason and the error are text the model or
// a service wrote, so they are escaped; the parameters are shown as
// action.DisplayParams writes them.
func proposalBlocks(u output.Update) []block {
	if len(u.Proposals) == 0 {
		return nil
	}

	ordered := slices.Clone(u.Proposals)
	slices.SortStableFunc(ordered, func(a, b action.Proposal) int {
		return proposalRank(a) - proposalRank(b)
	})

	shown := ordered[:min(len(ordered), maxProposalsShown)]

	var blocks []block

	for _, p := range shown {
		if p.Status == action.StatusProposed {
			blocks = append(blocks, pendingProposal(u, p)...)

			continue
		}

		blocks = append(blocks, contextBlock(proposalLine(p)))
	}

	if more := len(ordered) - len(shown); more > 0 {
		blocks = append(blocks, contextBlock(joinParts(fmt.Sprintf("%d more action proposals", more), openLink(u))))
	}

	return blocks
}

// proposalRank orders proposals: those that wait first, then the others in
// the order they came.
func proposalRank(p action.Proposal) int {
	if p.Status == action.StatusProposed {
		return 0
	}

	return 1
}

// pendingProposal renders a proposal that waits for a decision.
func pendingProposal(u output.Update, p action.Proposal) []block {
	blocks := []block{section(fmt.Sprintf(":bulb: *Proposed action: %s*\n%s", escape(cut(p.Action, maxTitleChars)), expiryLine(p)))}

	text := escape(p.ParamsText)
	complete := len([]rune(text)) <= maxParamsChars

	if complete {
		for i, chunk := range chunksOf(text, maxParamsChunk) {
			heading := ""
			if i == 0 {
				heading = "*Parameters, exactly as they will run*\n"
			}

			blocks = append(blocks, section(heading+"```"+chunk+"```"))
		}
	} else {
		blocks = append(blocks, section(fmt.Sprintf(
			":warning: The parameters are too long to show here (%d characters). Open the investigation in Sidekick and read them all before you decide.",
			len([]rune(p.ParamsText)))))
	}

	if reason := strings.TrimSpace(p.Reason); reason != "" {
		blocks = append(blocks, section("*Reason, written by the model and not checked*\n"+escape(strings.ReplaceAll(cut(reason, maxReasonChars), "`", "'"))))
	}

	return append(blocks, proposalButtons(u, p, complete), contextBlock(joinParts("Needs the approver role", openLink(u))))
}

// proposalButtons is the row with the Reject button and, when the whole
// parameter text is shown, the Approve button, which asks again before it
// approves.
func proposalButtons(_ output.Update, p action.Proposal, complete bool) block {
	var buttons []button

	if complete {
		buttons = append(buttons, button{
			Type: elemButton, ActionID: ActionApproveProposal, Text: plain("Approve"), Value: p.ID, Style: stylePrimary,
			Confirm: &confirmDialog{
				Title:   plain("Approve this action?"),
				Text:    plain("Sidekick runs " + cut(p.Action, maxTitleChars) + " with the parameters shown above."),
				Confirm: plain("Approve"),
				Deny:    plain("Not yet"),
			},
		})
	}

	buttons = append(buttons, button{Type: elemButton, ActionID: ActionRejectProposal, Text: plain("Reject"), Value: p.ID, Style: styleDanger})

	return block{Type: blockActions, BlockID: blockIDProposal + ":" + p.ID, Buttons: buttons}
}

// expiryLine says until when the proposal can be decided.
func expiryLine(p action.Proposal) string {
	return fmt.Sprintf("Mode %s · Decide before <!date^%d^{date_short_pretty} {time}|%s UTC>",
		escape(string(p.Mode)), p.ExpiresAt.Unix(), p.ExpiresAt.UTC().Format("2006-01-02 15:04"))
}

// proposalLine is the context line of a proposal that is not waiting: what
// became of it, and who decided.
func proposalLine(p action.Proposal) string {
	name := "*" + escape(cut(p.Action, maxTitleChars)) + "*"
	by := decider(p)

	switch p.Status {
	case action.StatusApproved:
		return ":white_check_mark: " + name + " approved" + byPart(by) + ", waiting to run"
	case action.StatusRejected:
		return ":x: " + name + " rejected" + byPart(by)
	case action.StatusExpired:
		return ":hourglass: " + name + " expired: nobody decided in time"
	case action.StatusExecuting:
		return ":gear: " + name + " approved" + byPart(by) + ", running now"
	case action.StatusSucceeded:
		return ":white_check_mark: " + name + " done" + byPart(by) + resultPart(p)
	case action.StatusFailed:
		detail := ""
		if p.Error != "" {
			detail = ": " + escape(cut(p.Error, maxResultChars))
		}

		return ":warning: " + name + " failed" + byPart(by) + detail
	default:
		return name + " " + escape(string(p.Status))
	}
}

// byPart is " by <who>", or nothing when nobody is known.
func byPart(by string) string {
	if by == "" {
		return ""
	}

	return " by " + by
}

// decider is the mention of the Slack user who decided, or the escaped
// name of the web user or token that did.
func decider(p action.Proposal) string {
	return mention(output.Person{SlackUserID: p.DecidedBySlackUser, Name: p.DecidedByName})
}

// resultPart is the summary of what an action did, with its links.
func resultPart(p action.Proposal) string {
	if p.Result == nil {
		return ""
	}

	parts := []string{escape(cut(p.Result.Summary, maxResultChars))}

	for _, l := range p.Result.Links {
		if rendered := link(l.URL, cut(l.Title, maxTitleChars)); rendered != "" {
			parts = append(parts, rendered)
		}
	}

	return ": " + strings.Join(parts, " · ")
}

// chunksOf cuts text into pieces of at most size characters, at line ends
// where it can.
func chunksOf(text string, size int) []string {
	runes := []rune(text)

	var chunks []string

	for len(runes) > size {
		cutAt := size

		if i := lastNewline(runes[:size]); i > size/2 {
			cutAt = i + 1
		}

		chunks = append(chunks, string(runes[:cutAt]))
		runes = runes[cutAt:]
	}

	return append(chunks, string(runes))
}

// lastNewline returns the index of the last newline in runes, or -1.
func lastNewline(runes []rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '\n' {
			return i
		}
	}

	return -1
}

// Decider names who decided the proposal, for a note to someone who
// clicked second: a Slack mention, or the escaped name of the web user or
// token. It is empty when nobody is known.
func Decider(p action.Proposal) string {
	return decider(p)
}

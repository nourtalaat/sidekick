package slack

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The words that stand in for a side of a change that has no value. They
// cannot be mistaken for a value: a string is shown in quotes.
const (
	notSet  = "(not set)"
	removed = "(removed)"
	wholeAs = "(the whole definition)"
)

// sides are the words that name the two values of a change.
type sides struct{ before, after string }

// The words for a change, and for what an undo would overwrite.
var (
	changedSides = sides{before: "before", after: "after"}
	restoreSides = sides{before: "now", after: "restored to"}
)

// changeBlocks renders the changes under a heading, one code block for each
// chunk of text that fits in a Slack section. It reports whether all of
// them are shown; when not, it shows none, because a person must never
// decide on a list that was cut.
func changeBlocks(heading string, changes []action.Change, labels sides) ([]block, bool) {
	text := changeText(changes, labels)

	if escapedLen(text) > maxParamsChars {
		return nil, false
	}

	var blocks []block

	for i, chunk := range chunksOf(text, maxParamsChunk) {
		prefix := ""
		if i == 0 {
			prefix = heading + "\n"
		}

		blocks = append(blocks, section(prefix+"```"+chunk+"```"))
	}

	return blocks, true
}

// changeText writes the changes, one after the other, as
//
//	options.thresholds.critical
//	  before: 2
//	  after:  3
//
// with the words of labels. The values are text that the action spelled out, so nothing in them can
// close the code block. The caller escapes the whole text.
func changeText(changes []action.Change, labels sides) string {
	width := max(len(labels.before), len(labels.after))
	entries := make([]string, 0, len(changes))

	for _, c := range changes {
		path := c.Path
		if path == "" {
			path = wholeAs
		}

		before, after := notSet, removed
		if c.Before != nil {
			before = *c.Before
		}

		if c.After != nil {
			after = *c.After
		}

		entries = append(entries, fmt.Sprintf("%s\n  %-*s %s\n  %-*s %s", path, width+1, labels.before+":", before, width+1, labels.after+":", after))
	}

	return strings.Join(entries, "\n\n")
}

// UndoConflict returns the fallback text and the blocks of the private
// message that shows what restoring the proposal with id would overwrite,
// with a Restore anyway button, for a click on Undo that found the
// definition changed after Sidekick changed it. The blocks are JSON, ready
// for the "blocks" member of a response. When the changes are too long to
// show, the blocks say so and have no button: a person must not confirm a
// list they cannot read.
func UndoConflict(id string, changes []action.Change) (string, json.RawMessage, error) {
	const text = "The definition changed after Sidekick changed it. Nothing was restored."

	blocks := []block{section(":warning: *The definition changed after Sidekick changed it.* Nothing was restored yet.")}

	shown, all := changeBlocks("Restoring puts these fields back, and overwrites what they are now:", changes, restoreSides)
	if !all {
		blocks = append(blocks, section("The fields are too long to show here. Open the investigation in Sidekick to read them before you restore."))
	} else {
		blocks = append(blocks, shown...)
		blocks = append(blocks, block{Type: blockActions, BlockID: blockIDUndo + ":" + id, Buttons: []button{{
			Type: elemButton, ActionID: ActionUndoProposalForce, Text: plain("Restore anyway"), Value: id, Style: styleDanger,
			Confirm: &confirmDialog{
				Title:   plain("Restore anyway?"),
				Text:    plain("Sidekick overwrites the fields shown with the values from before its change."),
				Confirm: plain("Restore"),
				Deny:    plain("Not now"),
			},
		}}})
	}

	raw, err := marshal(blocks)
	if err != nil {
		return "", nil, fmt.Errorf("encode the blocks of the undo conflict: %w", err)
	}

	return text, raw, nil
}

package investigator

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// ToolResult is one saved tool result, as the model read it.
type ToolResult struct {
	// CallID is the ID of the model's tool call.
	CallID string
	// Source is the source that answered, and empty for the built-in
	// tools.
	Source string
	// Tool is the tool's name in its source, or the built-in tool's name.
	Tool string
	// Status is how the call ended, as steps.status records it, for
	// example ok, no_data or unavailable.
	Status string
	// Content is the text the model read.
	Content string
	// IsError is true when the model read the result as an error.
	IsError bool
}

// ToolResults returns the tool results among the saved steps of a run, in
// the order they were saved, for readers of a run outside this package,
// such as the benchmark.
func ToolResults(steps []db.Step) ([]ToolResult, error) {
	var out []ToolResult

	for _, s := range steps {
		if s.Kind != kindToolResult {
			continue
		}

		parts, err := decodeParts(s.Parts)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", s.Seq, err)
		}

		r := ToolResult{CallID: deref(s.ToolCallID), Source: deref(s.Source), Tool: deref(s.Tool), Status: s.Status}

		for _, p := range parts {
			if tr, ok := p.(model.ToolResult); ok {
				r.Content, r.IsError = tr.Content, tr.IsError
			}
		}

		out = append(out, r)
	}

	return out, nil
}

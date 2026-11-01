package investigator

import (
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/source"
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

// RecordedCall is one call to a source tool as the saved steps record it:
// what the model asked and what the source answered.
type RecordedCall struct {
	// Seq is the sequence number of the result step.
	Seq int32
	// CallID is the ID of the model's tool call.
	CallID string
	// Source is the source that answered, and Tool its own name for the
	// tool.
	Source string
	Tool   string
	// Name is the qualified name the model called the tool by.
	Name string
	// Input is the JSON object of arguments the model sent. It is empty
	// when the call is not in the saved steps.
	Input json.RawMessage
	// Status is how the call ended, as steps.status records it.
	Status string
	// Text is the redacted result the model read, without the untrusted
	// data markers. It is still untrusted data.
	Text string
	// Evidence are the links the source gave.
	Evidence []source.Evidence
}

// RecordedCalls returns the calls to source tools among the saved steps,
// in the order their results were saved, each joined with the input of the
// model's call. Calls to built-in tools are left out, and so are summary
// steps, so Text is the result as the source gave it. Readers outside this
// package, such as the scenario export, use it.
func RecordedCalls(steps []db.Step) ([]RecordedCall, error) {
	inputs := map[string]model.ToolCall{}

	var out []RecordedCall

	for _, s := range steps {
		switch s.Kind {
		case kindModelTurn:
			parts, err := decodeParts(s.Parts)
			if err != nil {
				return nil, fmt.Errorf("step %d: %w", s.Seq, err)
			}

			for _, p := range parts {
				if call, ok := p.(model.ToolCall); ok {
					inputs[call.ID] = call
				}
			}
		case kindToolResult:
			if s.Source == nil || s.ToolCallID == nil {
				continue
			}

			call, err := recordedCall(s, inputs[*s.ToolCallID])
			if err != nil {
				return nil, fmt.Errorf("read the call of step %d: %w", s.Seq, err)
			}

			out = append(out, call)
		}
	}

	return out, nil
}

// recordedCall builds the record of one tool result step.
func recordedCall(s db.Step, call model.ToolCall) (RecordedCall, error) {
	parts, err := decodeParts(s.Parts)
	if err != nil {
		return RecordedCall{}, fmt.Errorf("step %d: %w", s.Seq, err)
	}

	out := RecordedCall{
		Seq: s.Seq, CallID: *s.ToolCallID, Source: *s.Source, Tool: deref(s.Tool),
		Name: call.Name, Input: call.Input, Status: s.Status,
	}

	for _, p := range parts {
		if tr, ok := p.(model.ToolResult); ok {
			out.Text = tr.Content
			if text, ok := prompt.UnwrapUntrusted(tr.Content); ok {
				out.Text = text
			}
		}
	}

	if len(s.Evidence) > 0 {
		if err := json.Unmarshal(s.Evidence, &out.Evidence); err != nil {
			return RecordedCall{}, fmt.Errorf("decode the evidence of step %d: %w", s.Seq, err)
		}
	}

	return out, nil
}

package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// InputSchema is a parsed tool input schema. The investigator checks the
// input of its built-in tools with it, the same way the guard checks the
// input of source tools.
type InputSchema struct {
	resolved *jsonschema.Resolved
}

// Check checks the model's input against the schema. The input must be a
// JSON object; empty input is the empty object, which Check returns in its
// place. The error explains the mismatch, for the model to fix.
func (s *InputSchema) Check(input json.RawMessage) (json.RawMessage, error) {
	return checkInput(s.resolved, input)
}

// ParseInputSchema parses and resolves a JSON Schema for tool input. An
// empty schema accepts any object.
func ParseInputSchema(raw json.RawMessage) (*InputSchema, error) {
	resolved, err := resolveSchema(raw)
	if err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}

	return &InputSchema{resolved: resolved}, nil
}

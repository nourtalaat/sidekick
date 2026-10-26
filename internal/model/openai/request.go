package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/model"
)

// systemSeparator joins the system text blocks into one instruction text.
const systemSeparator = "\n\n"

// emptyInput is the input of a tool call that has none.
var emptyInput = json.RawMessage(`{}`)

// emptySchema is the parameter schema of a tool that declares none.
var emptySchema = map[string]any{"type": "object", "properties": map[string]any{}}

// Errors for requests the connector cannot send.
var (
	errForcedToolChoice  = errors.New("this model or server does not accept a forced tool choice")
	errNoToolName        = errors.New("tool choice tool needs a tool name")
	errToolCallFromUser  = errors.New("a user message cannot hold a tool call")
	errToolResultFromBot = errors.New("an assistant message cannot hold a tool result")
	errUnknownRole       = errors.New("unknown message role")
)

// checkRequest rejects a request the model cannot take, before it is sent.
func (c *Connector) checkRequest(req model.Request) error {
	var errs []error

	if req.Thinking.Mode != "" {
		errs = append(errs, errThinking)
	}

	if effort := c.effort(req); !c.caps.SupportsEffort(effort) {
		errs = append(errs, fmt.Errorf("effort: %q is not supported by %s", effort, c.cfg.Model))
	}

	switch req.ToolChoice.Mode {
	case model.ToolChoiceAny, model.ToolChoiceRequired:
		if !c.caps.ToolChoice {
			errs = append(errs, errForcedToolChoice)
		}
	}

	if req.ToolChoice.Mode == model.ToolChoiceRequired && req.ToolChoice.Name == "" {
		errs = append(errs, errNoToolName)
	}

	for _, m := range req.Messages {
		if m.Role != model.User && m.Role != model.Assistant {
			errs = append(errs, fmt.Errorf("%q: %w", m.Role, errUnknownRole))
		}

		for _, p := range m.Parts {
			switch p.(type) {
			case model.ToolCall:
				if m.Role != model.Assistant {
					errs = append(errs, errToolCallFromUser)
				}
			case model.ToolResult:
				if m.Role != model.User {
					errs = append(errs, errToolResultFromBot)
				}
			}
		}
	}

	return errors.Join(errs...)
}

// effort returns the request's effort, else the config's.
func (c *Connector) effort(req model.Request) model.Effort {
	if req.Effort != model.EffortDefault {
		return req.Effort
	}

	return c.cfg.Effort
}

// temperature returns the request's temperature, else the config's.
func (c *Connector) temperature(req model.Request) *float64 {
	if req.Temperature != nil {
		return req.Temperature
	}

	return c.cfg.Temperature
}

// systemText joins the system text blocks.
func systemText(texts []model.Text) string {
	parts := make([]string, 0, len(texts))
	for _, t := range texts {
		parts = append(parts, t.Text)
	}

	return strings.Join(parts, systemSeparator)
}

// toolParameters decodes a tool's JSON Schema for the SDK, or returns an
// empty object schema when the tool declares none.
func toolParameters(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return emptySchema, nil
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}

	return schema, nil
}

// toolArguments returns the input of a tool call as the JSON text the APIs
// expect, or an empty object when there is none.
func toolArguments(in json.RawMessage) string {
	if len(in) == 0 {
		return string(emptyInput)
	}

	return string(in)
}

// toolInput turns the arguments a model sent into a tool call input. Empty
// arguments become an empty object. Arguments that are not valid JSON,
// which small local models sometimes write, are kept as a JSON string, so
// the call is still recorded and the tool rejects it with a message the
// model can act on.
func toolInput(arguments string) json.RawMessage {
	if strings.TrimSpace(arguments) == "" {
		return emptyInput
	}

	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}

	quoted, err := json.Marshal(arguments)
	if err != nil {
		return emptyInput
	}

	return quoted
}

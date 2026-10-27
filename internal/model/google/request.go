package google

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"

	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Content roles of the generateContent API.
const (
	roleUser  = "user"
	roleModel = "model"
)

// Keys of a function response, as the Gemini docs name them.
const (
	responseOutput = "output"
	responseError  = "error"
)

// dynamicBudget asks a Gemini 2.5 model to choose its own thinking budget.
const dynamicBudget = -1

// Kinds of a google Reasoning part, kept in Reasoning.ID. The API returns
// thought signatures inside the part they belong to, and they must go back
// in that same part.
const (
	// reasoningThought is a thought part: thought text with its signature.
	reasoningThought = "thought"
	// reasoningNext is the signature of the text or tool call part right
	// after it in the message.
	reasoningNext = "next"
	// reasoningPart is a part that holds only a signature.
	reasoningPart = "part"
)

// Request metadata is sent as Vertex AI labels. A label key starts with a
// lowercase letter; keys and values hold lowercase letters, digits,
// underscores and dashes, at most 63 of them.
var (
	labelKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	labelValuePattern = regexp.MustCompile(`^[a-z0-9_-]{0,63}$`)
)

// emptyArgs is the input of a tool call that has none.
var emptyArgs = json.RawMessage(`{}`)

// levels maps effort to the Gemini thinking levels.
var levels = map[model.Effort]genai.ThinkingLevel{
	model.EffortMinimal: genai.ThinkingLevelMinimal,
	model.EffortLow:     genai.ThinkingLevelLow,
	model.EffortMedium:  genai.ThinkingLevelMedium,
	model.EffortHigh:    genai.ThinkingLevelHigh,
}

// Errors for requests the connector cannot send.
var (
	errForcedToolChoice  = errors.New("this model does not accept a forced tool choice")
	errNoToolName        = errors.New("tool choice tool needs a tool name")
	errToolCallFromUser  = errors.New("a user message cannot hold a tool call")
	errToolResultFromBot = errors.New("an assistant message cannot hold a tool result")
	errUnknownRole       = errors.New("unknown message role")
	errUnknownCall       = errors.New("a tool result answers no tool call in the conversation")
	errTooManyTokens     = errors.New("maxTokens does not fit the generateContent API")
)

// request builds the contents and config of a generateContent call for
// req. Settings the request leaves zero come from the connector's config.
func (c *Connector) request(req model.Request) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	thinking := req.Thinking
	if thinking.Mode == "" {
		thinking = c.cfg.Thinking
	}

	effort := req.Effort
	if effort == model.EffortDefault {
		effort = c.cfg.Effort
	}

	temperature := req.Temperature
	if temperature == nil {
		temperature = c.cfg.Temperature
	}

	maxTokens := c.cfg.maxTokens(req.MaxTokens, c.model.caps)

	if err := c.checkRequest(req, thinking, effort, maxTokens); err != nil {
		return nil, nil, fmt.Errorf("check request: %w", err)
	}

	contents, err := contentParams(req.Messages)
	if err != nil {
		return nil, nil, fmt.Errorf("map messages: %w", err)
	}

	tools, err := toolParams(req.Tools)
	if err != nil {
		return nil, nil, fmt.Errorf("map tools: %w", err)
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction:     systemParam(req.System),
		MaxOutputTokens:       int32(maxTokens), //nolint:gosec // checkRequest rejects values above math.MaxInt32.
		Tools:                 tools,
		ToolConfig:            toolConfigParam(req.Tools, req.ToolChoice),
		ThinkingConfig:        c.thinkingParam(thinking, effort),
		AutomaticContinuation: genai.Ptr(false),
	}

	if temperature != nil {
		config.Temperature = genai.Ptr(float32(*temperature))
	}

	if c.cfg.backend() == BackendVertex {
		config.Labels = labels(req.Metadata)
	}

	return contents, config, nil
}

// checkRequest rejects a request the model cannot take, before it is sent.
func (c *Connector) checkRequest(req model.Request, thinking model.Thinking, effort model.Effort, maxTokens int) error {
	caps := c.model.caps

	var errs []error

	if !caps.SupportsThinking(thinking.Mode) {
		errs = append(errs, fmt.Errorf("thinking.mode: %q is not supported by %s", thinking.Mode, c.cfg.Model))
	}

	if !caps.SupportsEffort(effort) {
		errs = append(errs, fmt.Errorf("effort: %q is not supported by %s", effort, c.cfg.Model))
	}

	errs = append(errs, checkThinking(c.cfg.Model, c.model, thinking)...)

	if maxTokens > math.MaxInt32 {
		errs = append(errs, errTooManyTokens)
	}

	switch req.ToolChoice.Mode {
	case model.ToolChoiceAny, model.ToolChoiceRequired:
		if !caps.ToolChoice {
			errs = append(errs, errForcedToolChoice)
		}
	}

	if req.ToolChoice.Mode == model.ToolChoiceRequired && req.ToolChoice.Name == "" {
		errs = append(errs, errNoToolName)
	}

	return errors.Join(errs...)
}

// thinkingParam maps thinking and effort. A model with thinking levels
// takes effort as its level and thinks adaptively by default; a model with
// a thinking budget takes the budget, -1 for adaptive and 0 for off.
// Nothing set leaves the field out, so the model's default applies.
func (c *Connector) thinkingParam(t model.Thinking, effort model.Effort) *genai.ThinkingConfig {
	if c.model.budget == nil {
		level, ok := levels[effort]
		if !ok {
			return nil
		}

		return &genai.ThinkingConfig{ThinkingLevel: level}
	}

	switch t.Mode {
	case model.ThinkingAdaptive:
		return &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](dynamicBudget)}
	case model.ThinkingEnabled:
		return &genai.ThinkingConfig{ThinkingBudget: genai.Ptr(int32(t.BudgetTokens))} //nolint:gosec // checkThinking bounds the budget.
	case model.ThinkingOff:
		return &genai.ThinkingConfig{ThinkingBudget: genai.Ptr[int32](0)}
	default:
		return nil
	}
}

// contentParams maps the conversation. Reasoning from another provider is
// dropped, and a message left with no parts is left out.
func contentParams(msgs []model.Message) ([]*genai.Content, error) {
	names := callNames(msgs)
	out := make([]*genai.Content, 0, len(msgs))

	for i, m := range msgs {
		var role string

		switch m.Role {
		case model.User:
			role = roleUser
		case model.Assistant:
			role = roleModel
		default:
			return nil, fmt.Errorf("messages[%d]: %q: %w", i, m.Role, errUnknownRole)
		}

		parts, err := partParams(m, names)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}

		if len(parts) == 0 {
			continue
		}

		out = append(out, &genai.Content{Role: role, Parts: parts})
	}

	return out, nil
}

// partParams maps the parts of one message. Thought signatures go back in
// the part they came in, exactly as received.
func partParams(m model.Message, names map[string]string) ([]*genai.Part, error) {
	var (
		parts   []*genai.Part
		pending []byte
	)

	for i, part := range m.Parts {
		var (
			p   *genai.Part
			err error
		)

		switch v := part.(type) {
		case model.Text:
			p = &genai.Part{Text: v.Text}
		case model.ToolCall:
			if m.Role != model.Assistant {
				return nil, errToolCallFromUser
			}

			p, err = functionCallPart(v)
		case model.ToolResult:
			if m.Role != model.User {
				return nil, errToolResultFromBot
			}

			p, err = functionResponsePart(v, names)
		case model.Reasoning:
			if v.Provider != Provider {
				continue
			}

			sig, derr := base64.StdEncoding.DecodeString(v.Signature)
			if derr != nil {
				return nil, fmt.Errorf("thought signature: %w", derr)
			}

			switch v.ID {
			case reasoningNext:
				pending = sig
			case reasoningThought:
				parts = append(parts, &genai.Part{Text: v.Text, Thought: true, ThoughtSignature: sig})
			default:
				parts = append(parts, &genai.Part{ThoughtSignature: sig})
			}

			continue
		}

		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}

		p.ThoughtSignature, pending = pending, nil
		parts = append(parts, p)
	}

	return parts, nil
}

// functionCallPart maps a tool call. An ID the connector made up, for a
// call the API sent without one, is not sent back.
func functionCallPart(call model.ToolCall) (*genai.Part, error) {
	var args map[string]any
	if err := json.Unmarshal(cmpOrRaw(call.Input, emptyArgs), &args); err != nil {
		return nil, fmt.Errorf("tool call %s: input: %w", call.ID, err)
	}

	return &genai.Part{FunctionCall: &genai.FunctionCall{ID: apiCallID(call.ID), Name: call.Name, Args: args}}, nil
}

// functionResponsePart maps a tool result to a function response, which
// names the function it answers.
func functionResponsePart(r model.ToolResult, names map[string]string) (*genai.Part, error) {
	name, ok := names[r.CallID]
	if !ok {
		return nil, fmt.Errorf("tool result %s: %w", r.CallID, errUnknownCall)
	}

	key := responseOutput
	if r.IsError {
		key = responseError
	}

	return &genai.Part{FunctionResponse: &genai.FunctionResponse{
		ID:       apiCallID(r.CallID),
		Name:     name,
		Response: map[string]any{key: r.Content},
	}}, nil
}

// callNames maps the ID of every tool call in the conversation to its tool
// name.
func callNames(msgs []model.Message) map[string]string {
	names := map[string]string{}

	for _, m := range msgs {
		for _, p := range m.Parts {
			if call, ok := p.(model.ToolCall); ok {
				names[call.ID] = call.Name
			}
		}
	}

	return names
}

// toolParams maps the tools to function declarations, with their schemas as
// full JSON Schema.
func toolParams(specs []model.ToolSpec) ([]*genai.Tool, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	decls := make([]*genai.FunctionDeclaration, 0, len(specs))

	for _, s := range specs {
		d := &genai.FunctionDeclaration{Name: s.Name, Description: s.Description}

		if len(s.InputSchema) > 0 {
			if !json.Valid(s.InputSchema) {
				return nil, fmt.Errorf("tool %s: input schema is not JSON", s.Name)
			}

			d.ParametersJsonSchema = s.InputSchema
		}

		decls = append(decls, d)
	}

	return []*genai.Tool{{FunctionDeclarations: decls}}, nil
}

// toolConfigParam maps the tool choice. A request without tools, or
// without a tool choice, sends none.
func toolConfigParam(specs []model.ToolSpec, tc model.ToolChoice) *genai.ToolConfig {
	if len(specs) == 0 {
		return nil
	}

	fc := &genai.FunctionCallingConfig{}

	switch tc.Mode {
	case model.ToolChoiceAuto:
		fc.Mode = genai.FunctionCallingConfigModeAuto
	case model.ToolChoiceAny:
		fc.Mode = genai.FunctionCallingConfigModeAny
	case model.ToolChoiceRequired:
		fc.Mode = genai.FunctionCallingConfigModeAny
		fc.AllowedFunctionNames = []string{tc.Name}
	case model.ToolChoiceNone:
		fc.Mode = genai.FunctionCallingConfigModeNone
	default:
		return nil
	}

	return &genai.ToolConfig{FunctionCallingConfig: fc}
}

// systemParam maps the system text, or nil when there is none.
func systemParam(texts []model.Text) *genai.Content {
	if len(texts) == 0 {
		return nil
	}

	parts := make([]*genai.Part, 0, len(texts))
	for _, t := range texts {
		parts = append(parts, &genai.Part{Text: t.Text})
	}

	return &genai.Content{Role: roleUser, Parts: parts}
}

// labels returns the request metadata that is valid as Vertex AI labels.
// Other entries are left out rather than failing the call.
func labels(metadata map[string]string) map[string]string {
	out := map[string]string{}

	for k, v := range metadata {
		if labelKeyPattern.MatchString(k) && labelValuePattern.MatchString(v) {
			out[k] = v
		}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// cmpOrRaw returns raw, or def when raw is empty.
func cmpOrRaw(raw, def json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return def
	}

	return raw
}

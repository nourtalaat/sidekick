package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/nourtalaat/sidekick/internal/model"
)

// maxCacheBreakpoints is how many cache_control marks one request may carry.
const maxCacheBreakpoints = 4

// emptyInput is the input of a tool call that has none.
var emptyInput = json.RawMessage(`{}`)

// Errors for requests the connector cannot send.
var (
	errTooManyBreakpoints = fmt.Errorf("more than %d prompt cache breakpoints", maxCacheBreakpoints)
	errForcedToolChoice   = errors.New("this model does not accept a forced tool choice")
	errNoToolName         = errors.New("tool choice tool needs a tool name")
	errToolCallFromUser   = errors.New("a user message cannot hold a tool call")
	errToolResultFromBot  = errors.New("an assistant message cannot hold a tool result")
	errUnknownRole        = errors.New("unknown message role")
)

// params builds the Messages API request for req. Settings the request
// leaves zero come from the connector's config.
func (c *Connector) params(req model.Request) (anthropic.MessageNewParams, error) {
	var zero anthropic.MessageNewParams

	if req.Temperature != nil {
		return zero, errTemperature
	}

	thinking := req.Thinking
	if thinking.Mode == "" {
		thinking = c.cfg.Thinking
	}

	effort := req.Effort
	if effort == model.EffortDefault {
		effort = c.cfg.Effort
	}

	maxTokens := c.cfg.maxTokens(req.MaxTokens, c.model.caps)

	if err := c.checkRequest(req, thinking, effort, maxTokens); err != nil {
		return zero, err
	}

	messages, err := messageParams(req.Messages)
	if err != nil {
		return zero, err
	}

	tools, err := toolParams(req.Tools)
	if err != nil {
		return zero, err
	}

	p := anthropic.MessageNewParams{
		Model:      c.cfg.Model,
		MaxTokens:  int64(maxTokens),
		System:     textParams(req.System),
		Messages:   messages,
		Tools:      tools,
		ToolChoice: toolChoiceParam(c.toolChoice(req.ToolChoice, thinking)),
		Thinking:   c.thinkingParam(thinking),
	}

	if effort != model.EffortDefault {
		p.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(effort)}
	}

	return p, nil
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

	errs = append(errs, checkThinking(c.cfg.Model, c.model, thinking, effort, maxTokens)...)

	if forced(req.ToolChoice.Mode) && !caps.ToolChoice {
		errs = append(errs, errForcedToolChoice)
	}

	if req.ToolChoice.Mode == model.ToolChoiceRequired && req.ToolChoice.Name == "" {
		errs = append(errs, errNoToolName)
	}

	if countBreakpoints(req) > maxCacheBreakpoints {
		errs = append(errs, errTooManyBreakpoints)
	}

	return errors.Join(errs...)
}

// toolChoice returns the tool choice to send. The API cannot force a tool
// while the model thinks, so a forced choice becomes auto then, and thinking
// and the conversation stay as they are.
func (c *Connector) toolChoice(tc model.ToolChoice, thinking model.Thinking) model.ToolChoice {
	if !forced(tc.Mode) || !c.thinks(thinking) {
		return tc
	}

	c.logger.Debug("sending tool choice auto: a tool cannot be forced while the model thinks",
		"connector", c.name, "model", c.cfg.Model, "tool_choice", string(tc.Mode))

	return model.ToolChoice{Mode: model.ToolChoiceAuto}
}

// thinks reports whether the model thinks in a call with thinking t.
func (c *Connector) thinks(t model.Thinking) bool {
	switch t.Mode {
	case model.ThinkingAdaptive, model.ThinkingEnabled:
		return true
	case "":
		return c.model.thinksByDefault
	default:
		return false
	}
}

// thinkingParam maps the thinking setting. An unset mode leaves the
// parameter out, so the model's default applies; off follows the model's
// off rule.
func (c *Connector) thinkingParam(t model.Thinking) anthropic.ThinkingConfigParamUnion {
	switch t.Mode {
	case model.ThinkingAdaptive:
		return anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
	case model.ThinkingEnabled:
		return anthropic.ThinkingConfigParamOfEnabled(int64(t.BudgetTokens))
	case model.ThinkingOff:
		if c.model.off == offDisable || c.model.off == offDisableUpToHigh {
			return anthropic.ThinkingConfigParamUnion{OfDisabled: &anthropic.ThinkingConfigDisabledParam{}}
		}
	}

	return anthropic.ThinkingConfigParamUnion{}
}

// messageParams maps the conversation. Reasoning from another provider is
// dropped, and a message left with no parts is left out.
func messageParams(msgs []model.Message) ([]anthropic.MessageParam, error) {
	out := make([]anthropic.MessageParam, 0, len(msgs))

	for i, m := range msgs {
		blocks, err := contentParams(m)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}

		if len(blocks) == 0 {
			continue
		}

		switch m.Role {
		case model.User:
			out = append(out, anthropic.NewUserMessage(blocks...))
		case model.Assistant:
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("messages[%d]: %q: %w", i, m.Role, errUnknownRole)
		}
	}

	return out, nil
}

// contentParams maps the parts of one message. Thinking blocks go back
// exactly as they came: the same text, signature and redacted data.
func contentParams(m model.Message) ([]anthropic.ContentBlockParamUnion, error) {
	var blocks []anthropic.ContentBlockParamUnion

	for _, part := range m.Parts {
		switch p := part.(type) {
		case model.Text:
			blocks = append(blocks, anthropic.ContentBlockParamUnion{OfText: textParam(p)})
		case model.ToolCall:
			if m.Role != model.Assistant {
				return nil, errToolCallFromUser
			}

			blocks = append(blocks, anthropic.ContentBlockParamUnion{OfToolUse: &anthropic.ToolUseBlockParam{
				ID:    p.ID,
				Name:  p.Name,
				Input: toolInput(p.Input),
			}})
		case model.ToolResult:
			if m.Role != model.User {
				return nil, errToolResultFromBot
			}

			blocks = append(blocks, anthropic.ContentBlockParamUnion{OfToolResult: toolResultParam(p)})
		case model.Reasoning:
			if b, ok := reasoningParam(p); ok {
				blocks = append(blocks, b)
			}
		}
	}

	return blocks, nil
}

// reasoningParam maps a reasoning part, and false when it came from another
// provider.
func reasoningParam(r model.Reasoning) (anthropic.ContentBlockParamUnion, bool) {
	if r.Provider != Provider {
		return anthropic.ContentBlockParamUnion{}, false
	}

	if r.Redacted != nil {
		return anthropic.ContentBlockParamUnion{OfRedactedThinking: &anthropic.RedactedThinkingBlockParam{Data: string(r.Redacted)}}, true
	}

	return anthropic.ContentBlockParamUnion{OfThinking: &anthropic.ThinkingBlockParam{Thinking: r.Text, Signature: r.Signature}}, true
}

// toolResultParam maps a tool result to a tool_result block with one text
// block.
func toolResultParam(r model.ToolResult) *anthropic.ToolResultBlockParam {
	p := &anthropic.ToolResultBlockParam{
		ToolUseID: r.CallID,
		Content:   []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: r.Content}}},
	}

	if r.IsError {
		p.IsError = anthropic.Bool(true)
	}

	return p
}

// toolParams maps the tool specs. Each tool's JSON Schema is sent with its
// keys in a stable order, so the tools stay a cacheable prefix.
func toolParams(specs []model.ToolSpec) ([]anthropic.ToolUnionParam, error) {
	var out []anthropic.ToolUnionParam

	for _, s := range specs {
		schema, err := inputSchema(s.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", s.Name, err)
		}

		t := &anthropic.ToolParam{Name: s.Name, InputSchema: schema}
		if s.Description != "" {
			t.Description = anthropic.String(s.Description)
		}

		if s.Cache {
			t.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}

		out = append(out, anthropic.ToolUnionParam{OfTool: t})
	}

	return out, nil
}

// inputSchema splits a JSON Schema object into the SDK's schema type. The
// properties and other keywords keep their JSON as it was written.
func inputSchema(raw json.RawMessage) (anthropic.ToolInputSchemaParam, error) {
	var schema anthropic.ToolInputSchemaParam
	if len(raw) == 0 {
		return schema, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return schema, fmt.Errorf("input schema: %w", err)
	}

	if props, ok := fields["properties"]; ok {
		schema.Properties = props
	}

	if req, ok := fields["required"]; ok {
		if err := json.Unmarshal(req, &schema.Required); err != nil {
			return schema, fmt.Errorf("input schema required: %w", err)
		}
	}

	extra := map[string]any{}

	for k, v := range fields {
		switch k {
		case "type", "properties", "required":
		default:
			extra[k] = v
		}
	}

	if len(extra) > 0 {
		schema.ExtraFields = extra
	}

	return schema, nil
}

// toolChoiceParam maps the tool choice. An unset mode leaves it out.
func toolChoiceParam(tc model.ToolChoice) anthropic.ToolChoiceUnionParam {
	switch tc.Mode {
	case model.ToolChoiceAuto:
		return anthropic.ToolChoiceUnionParam{OfAuto: &anthropic.ToolChoiceAutoParam{}}
	case model.ToolChoiceAny:
		return anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
	case model.ToolChoiceRequired:
		return anthropic.ToolChoiceUnionParam{OfTool: &anthropic.ToolChoiceToolParam{Name: tc.Name}}
	case model.ToolChoiceNone:
		return anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
	default:
		return anthropic.ToolChoiceUnionParam{}
	}
}

// textParams maps system text blocks.
func textParams(texts []model.Text) []anthropic.TextBlockParam {
	var out []anthropic.TextBlockParam
	for _, t := range texts {
		out = append(out, *textParam(t))
	}

	return out
}

// textParam maps one text part, with a cache breakpoint when it asks for one.
func textParam(t model.Text) *anthropic.TextBlockParam {
	p := &anthropic.TextBlockParam{Text: t.Text}
	if t.Cache {
		p.CacheControl = anthropic.NewCacheControlEphemeralParam()
	}

	return p
}

// toolInput returns the input of a tool call as raw JSON, or an empty
// object when there is none, which the API requires.
func toolInput(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return emptyInput
	}

	return in
}

// countBreakpoints counts the cache breakpoints a request asks for.
func countBreakpoints(req model.Request) int {
	n := 0

	for _, t := range req.System {
		if t.Cache {
			n++
		}
	}

	for _, s := range req.Tools {
		if s.Cache {
			n++
		}
	}

	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if t, ok := p.(model.Text); ok && t.Cache {
				n++
			}
		}
	}

	return n
}

// forced reports whether a tool choice mode makes the model call a tool.
func forced(mode model.ToolChoiceMode) bool {
	return mode == model.ToolChoiceAny || mode == model.ToolChoiceRequired
}

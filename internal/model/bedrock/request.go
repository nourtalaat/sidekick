package bedrock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithydocument "github.com/aws/smithy-go/document"

	"github.com/nourtalaat/sidekick/internal/model"
)

// maxCachePoints is how many cache points one Converse request may carry.
const maxCachePoints = 4

// Fields of additionalModelRequestFields in Anthropic's request shape.
const (
	fieldThinking     = "thinking"
	fieldOutputConfig = "output_config"
)

// Claude thinking types.
const (
	thinkingTypeAdaptive = "adaptive"
	thinkingTypeEnabled  = "enabled"
	thinkingTypeDisabled = "disabled"
)

// emptySchema is the input schema of a tool that declares none.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// emptyInput is the input of a tool call that has none.
var emptyInput = json.RawMessage(`{}`)

// Errors for requests the connector cannot send.
var (
	errTooManyCachePoints = fmt.Errorf("more than %d prompt cache points", maxCachePoints)
	errForcedToolChoice   = errors.New("this model does not accept a forced tool choice")
	errNoToolName         = errors.New("tool choice tool needs a tool name")
	errToolChoiceNone     = errors.New("tool choice none: the Converse API has no way to turn tools off for one call; leave tool choice unset or use auto")
	errToolCallFromUser   = errors.New("a user message cannot hold a tool call")
	errToolResultFromBot  = errors.New("an assistant message cannot hold a tool result")
	errUnknownRole        = errors.New("unknown message role")
	errTooManyTokens      = errors.New("maxTokens does not fit the Converse API")
)

// converseInput holds the fields that Converse and ConverseStream share.
type converseInput struct {
	messages   []types.Message
	system     []types.SystemContentBlock
	tools      *types.ToolConfiguration
	inference  *types.InferenceConfiguration
	additional document.Interface
	maxTokens  int
}

// stream reports whether the call should stream: when its output limit is
// large or left to the model.
func (in converseInput) stream() bool {
	return in.maxTokens == 0 || in.maxTokens > streamAbove
}

// input builds the Converse request for req. Settings the request leaves
// zero come from the connector's config.
func (c *Connector) input(req model.Request) (converseInput, error) {
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
		return converseInput{}, err
	}

	cache := c.model.caps.PromptCaching

	messages, err := messageParams(req.Messages, cache, c.anthropic)
	if err != nil {
		return converseInput{}, err
	}

	tools, err := c.toolConfig(req, thinking, cache)
	if err != nil {
		return converseInput{}, err
	}

	return converseInput{
		messages:   messages,
		system:     systemParams(req.System, cache),
		tools:      tools,
		inference:  inferenceParams(maxTokens, temperature),
		additional: c.additionalFields(thinking, effort),
		maxTokens:  maxTokens,
	}, nil
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

	if maxTokens > math.MaxInt32 {
		errs = append(errs, errTooManyTokens)
	}

	switch req.ToolChoice.Mode {
	case model.ToolChoiceNone:
		errs = append(errs, errToolChoiceNone)
	case model.ToolChoiceAny, model.ToolChoiceRequired:
		if !caps.ToolChoice {
			errs = append(errs, errForcedToolChoice)
		}
	}

	if req.ToolChoice.Mode == model.ToolChoiceRequired && req.ToolChoice.Name == "" {
		errs = append(errs, errNoToolName)
	}

	if caps.PromptCaching && countCachePoints(req) > maxCachePoints {
		errs = append(errs, errTooManyCachePoints)
	}

	return errors.Join(errs...)
}

// toolConfig maps the tools and the tool choice. A request without tools
// sends no tool config, and so no tool choice.
func (c *Connector) toolConfig(req model.Request, thinking model.Thinking, cache bool) (*types.ToolConfiguration, error) {
	if len(req.Tools) == 0 {
		return nil, nil
	}

	tools := make([]types.Tool, 0, len(req.Tools))

	for _, s := range req.Tools {
		schema, err := jsonDocument(cmpOrRaw(s.InputSchema, emptySchema))
		if err != nil {
			return nil, fmt.Errorf("tool %s: input schema: %w", s.Name, err)
		}

		spec := types.ToolSpecification{Name: aws.String(s.Name), InputSchema: &types.ToolInputSchemaMemberJson{Value: schema}}
		if s.Description != "" {
			spec.Description = aws.String(s.Description)
		}

		tools = append(tools, &types.ToolMemberToolSpec{Value: spec})

		if s.Cache && cache {
			tools = append(tools, &types.ToolMemberCachePoint{Value: cachePoint()})
		}
	}

	return &types.ToolConfiguration{Tools: tools, ToolChoice: toolChoiceParam(c.toolChoice(req.ToolChoice, thinking))}, nil
}

// toolChoice returns the tool choice to send. Claude cannot force a tool
// while it thinks, so a forced choice becomes auto then, and thinking and
// the conversation stay as they are.
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

// additionalFields maps thinking and effort to Claude's request fields.
// Effort goes in output_config, never inside thinking. Other model
// families get neither; checkRequest has already rejected them there.
func (c *Connector) additionalFields(t model.Thinking, effort model.Effort) document.Interface {
	if !c.anthropic {
		return nil
	}

	fields := map[string]any{}

	if thinking := c.thinkingField(t); thinking != nil {
		fields[fieldThinking] = thinking
	}

	if effort != model.EffortDefault {
		fields[fieldOutputConfig] = map[string]any{"effort": string(effort)}
	}

	if len(fields) == 0 {
		return nil
	}

	return document.NewLazyDocument(fields)
}

// thinkingField maps the thinking setting. An unset mode leaves the field
// out, so the model's default applies; off follows the model's off rule.
func (c *Connector) thinkingField(t model.Thinking) map[string]any {
	switch t.Mode {
	case model.ThinkingAdaptive:
		return map[string]any{"type": thinkingTypeAdaptive}
	case model.ThinkingEnabled:
		return map[string]any{"type": thinkingTypeEnabled, "budget_tokens": t.BudgetTokens}
	case model.ThinkingOff:
		if c.model.off == offDisable || c.model.off == offDisableUpToHigh {
			return map[string]any{"type": thinkingTypeDisabled}
		}
	}

	return nil
}

// messageParams maps the conversation. Reasoning from another provider is
// dropped, and a message left with no parts is left out.
func messageParams(msgs []model.Message, cache, anthropic bool) ([]types.Message, error) {
	out := make([]types.Message, 0, len(msgs))

	for i, m := range msgs {
		var role types.ConversationRole

		switch m.Role {
		case model.User:
			role = types.ConversationRoleUser
		case model.Assistant:
			role = types.ConversationRoleAssistant
		default:
			return nil, fmt.Errorf("messages[%d]: %q: %w", i, m.Role, errUnknownRole)
		}

		blocks, err := contentParams(m, cache, anthropic)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}

		if len(blocks) == 0 {
			continue
		}

		out = append(out, types.Message{Role: role, Content: blocks})
	}

	return out, nil
}

// contentParams maps the parts of one message. Reasoning goes back exactly
// as it came, in its place: the same text, signature and redacted data.
func contentParams(m model.Message, cache, anthropic bool) ([]types.ContentBlock, error) {
	var blocks []types.ContentBlock

	for _, part := range m.Parts {
		switch p := part.(type) {
		case model.Text:
			blocks = append(blocks, &types.ContentBlockMemberText{Value: p.Text})
			if p.Cache && cache {
				blocks = append(blocks, &types.ContentBlockMemberCachePoint{Value: cachePoint()})
			}
		case model.ToolCall:
			if m.Role != model.Assistant {
				return nil, errToolCallFromUser
			}

			input, err := jsonDocument(cmpOrRaw(p.Input, emptyInput))
			if err != nil {
				return nil, fmt.Errorf("tool call %s: input: %w", p.ID, err)
			}

			blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
				ToolUseId: aws.String(p.ID),
				Name:      aws.String(p.Name),
				Input:     input,
			}})
		case model.ToolResult:
			if m.Role != model.User {
				return nil, errToolResultFromBot
			}

			blocks = append(blocks, &types.ContentBlockMemberToolResult{Value: toolResultParam(p, anthropic)})
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
func reasoningParam(r model.Reasoning) (types.ContentBlock, bool) {
	if r.Provider != Provider {
		return nil, false
	}

	if r.Redacted != nil {
		return &types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberRedactedContent{Value: r.Redacted}}, true
	}

	text := types.ReasoningTextBlock{Text: aws.String(r.Text)}
	if r.Signature != "" {
		text.Signature = aws.String(r.Signature)
	}

	return &types.ContentBlockMemberReasoningContent{Value: &types.ReasoningContentBlockMemberReasoningText{Value: text}}, true
}

// toolResultParam maps a tool result to a toolResult block with one text
// block. The error status is sent to Claude only; other families may
// reject the field.
func toolResultParam(r model.ToolResult, anthropic bool) types.ToolResultBlock {
	b := types.ToolResultBlock{
		ToolUseId: aws.String(r.CallID),
		Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: r.Content}},
	}

	if r.IsError && anthropic {
		b.Status = types.ToolResultStatusError
	}

	return b
}

// systemParams maps system text blocks, with a cache point after each one
// that asks for it.
func systemParams(texts []model.Text, cache bool) []types.SystemContentBlock {
	var out []types.SystemContentBlock

	for _, t := range texts {
		out = append(out, &types.SystemContentBlockMemberText{Value: t.Text})
		if t.Cache && cache {
			out = append(out, &types.SystemContentBlockMemberCachePoint{Value: cachePoint()})
		}
	}

	return out
}

// toolChoiceParam maps the tool choice. An unset mode leaves it out.
// checkRequest has already rejected tool choice none.
func toolChoiceParam(tc model.ToolChoice) types.ToolChoice {
	switch tc.Mode {
	case model.ToolChoiceAuto:
		return &types.ToolChoiceMemberAuto{}
	case model.ToolChoiceAny:
		return &types.ToolChoiceMemberAny{}
	case model.ToolChoiceRequired:
		return &types.ToolChoiceMemberTool{Value: types.SpecificToolChoice{Name: aws.String(tc.Name)}}
	default:
		return nil
	}
}

// inferenceParams maps the output limit and temperature, or nil when
// neither is set.
func inferenceParams(maxTokens int, temperature *float64) *types.InferenceConfiguration {
	if maxTokens == 0 && temperature == nil {
		return nil
	}

	cfg := &types.InferenceConfiguration{}
	if maxTokens > 0 {
		cfg.MaxTokens = aws.Int32(int32(maxTokens)) //nolint:gosec // checkRequest rejects values above math.MaxInt32.
	}

	if temperature != nil {
		cfg.Temperature = aws.Float32(float32(*temperature))
	}

	return cfg
}

// cachePoint is a cache point with the default 5-minute lifetime.
func cachePoint() types.CachePointBlock {
	return types.CachePointBlock{Type: types.CachePointTypeDefault}
}

// countCachePoints counts the cache points a request asks for.
func countCachePoints(req model.Request) int {
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

// jsonDocument decodes raw JSON into a document for the SDK. Numbers keep
// their exact text.
func jsonDocument(raw json.RawMessage) (document.Interface, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}

	return document.NewLazyDocument(documentValue(v)), nil
}

// documentValue turns JSON numbers into document numbers, which the SDK's
// encoder writes as numbers rather than strings.
func documentValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		return smithydocument.Number(x)
	case map[string]any:
		for k, e := range x {
			x[k] = documentValue(e)
		}

		return x
	case []any:
		for i, e := range x {
			x[i] = documentValue(e)
		}

		return x
	default:
		return v
	}
}

// cmpOrRaw returns raw, or def when raw is empty.
func cmpOrRaw(raw, def json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return def
	}

	return raw
}

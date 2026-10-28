package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Responses API item, content and event types the connector reads.
const (
	itemMessage      = "message"
	itemFunctionCall = "function_call"
	itemReasoning    = "reasoning"

	contentOutputText = "output_text"
	contentRefusal    = "refusal"

	eventCompleted  = "response.completed"
	eventIncomplete = "response.incomplete"
	eventFailed     = "response.failed"
	eventError      = "error"
)

// Reasons a response is incomplete.
const (
	incompleteMaxTokens     = "max_output_tokens"
	incompleteContentFilter = "content_filter"
)

// summarySeparator joins the parts of a reasoning summary into one text.
const summarySeparator = "\n\n"

// errNoFinalResponse is returned when the stream ended without the event
// that carries the whole response.
var errNoFinalResponse = errors.New("stream ended before the response was complete")

// generateResponses runs one turn on the Responses API.
func (c *Connector) generateResponses(ctx context.Context, req model.Request, opts []option.RequestOption) (model.Response, error) {
	params, err := c.responseParams(req)
	if err != nil {
		return model.Response{}, &buildError{err: err}
	}

	resp, requestID, err := c.streamResponse(ctx, params, opts)
	if err != nil {
		return model.Response{}, fmt.Errorf("response: %w", err)
	}

	out := fromResponse(resp, c.provider)
	out.RequestID = requestID

	return out, nil
}

// streamResponse sends params as a streamed request and returns the
// response from the final event, with the request ID from the headers.
func (c *Connector) streamResponse(ctx context.Context, params responses.ResponseNewParams, opts []option.RequestOption) (resp responses.Response, requestID string, err error) {
	var httpResp *http.Response

	s := c.responses.NewStreaming(ctx, params, append(opts, option.WithResponseInto(&httpResp))...)

	defer func() {
		if cerr := s.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	if httpResp != nil {
		requestID = requestIDOf(httpResp.Header)
	}

	var final *responses.Response

	for s.Next() {
		ev := s.Current()

		switch ev.Type {
		case eventCompleted, eventIncomplete:
			final = &ev.Response
		case eventFailed:
			return resp, requestID, &failedError{code: string(ev.Response.Error.Code), message: ev.Response.Error.Message}
		case eventError:
			return resp, requestID, &failedError{code: ev.Code, message: ev.Message}
		}
	}

	if serr := s.Err(); serr != nil {
		return resp, requestID, fmt.Errorf("stream response: %w", serr)
	}

	if final == nil {
		return resp, requestID, errNoFinalResponse
	}

	return *final, requestID, nil
}

// responseParams builds the Responses API request for req. Requests are
// stateless: store is false and every turn sends the whole conversation,
// with the reasoning items of earlier turns in their places, so no state
// at OpenAI is needed to resume or replay a run.
func (c *Connector) responseParams(req model.Request) (responses.ResponseNewParams, error) {
	input, err := inputItems(req.Messages, c.provider)
	if err != nil {
		return responses.ResponseNewParams{}, fmt.Errorf("map input items: %w", err)
	}

	tools, err := responseTools(req.Tools)
	if err != nil {
		return responses.ResponseNewParams{}, fmt.Errorf("map tools: %w", err)
	}

	p := responses.ResponseNewParams{
		Model:      c.model,
		Input:      responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Tools:      tools,
		ToolChoice: responseToolChoice(req.ToolChoice),
		Store:      openai.Bool(false),
	}

	if len(req.System) > 0 {
		p.Instructions = openai.String(systemText(req.System))
	}

	if n := maxTokens(req.MaxTokens, c.settings.MaxTokens, c.caps); n > 0 {
		p.MaxOutputTokens = openai.Int(int64(n))
	}

	if t := c.temperature(req); t != nil {
		p.Temperature = openai.Float(*t)
	}

	if effort := c.effort(req); effort != model.EffortDefault {
		p.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(effort)}
	}

	if len(req.Metadata) > 0 {
		p.Metadata = shared.Metadata(req.Metadata)
	}

	return p, nil
}

// inputItems maps the conversation to input items, in order. Consecutive
// text parts of one message become one message item; reasoning from
// another provider is dropped.
func inputItems(msgs []model.Message, provider string) (responses.ResponseInputParam, error) {
	var items responses.ResponseInputParam

	for i, m := range msgs {
		role := responses.EasyInputMessageRoleUser
		if m.Role == model.Assistant {
			role = responses.EasyInputMessageRoleAssistant
		}

		var texts []string

		flush := func() {
			if len(texts) > 0 {
				items = append(items, responses.ResponseInputItemParamOfMessage(strings.Join(texts, systemSeparator), role))
				texts = nil
			}
		}

		for _, part := range m.Parts {
			switch p := part.(type) {
			case model.Text:
				texts = append(texts, p.Text)
			case model.ToolCall:
				flush()

				items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					CallID:    p.ID,
					Name:      p.Name,
					Arguments: toolArguments(p.Input),
				}})
			case model.ToolResult:
				flush()

				items = append(items, responses.ResponseInputItemUnionParam{OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: openai.String(p.CallID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: openai.String(p.Content)},
				}})
			case model.Reasoning:
				if p.Provider != provider {
					continue
				}

				flush()

				items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: reasoningItem(p)})
			default:
				return nil, fmt.Errorf("messages[%d]: unknown part %T", i, part)
			}
		}

		flush()
	}

	return items, nil
}

// reasoningItem maps a reasoning part back to the item it came from: the
// same item ID, encrypted content and summary text.
func reasoningItem(r model.Reasoning) *responses.ResponseReasoningItemParam {
	item := &responses.ResponseReasoningItemParam{
		ID:      r.ID,
		Summary: []responses.ResponseReasoningItemSummaryParam{},
	}

	if r.Text != "" {
		item.Summary = append(item.Summary, responses.ResponseReasoningItemSummaryParam{Text: r.Text})
	}

	if r.Signature != "" {
		item.EncryptedContent = openai.String(r.Signature)
	}

	return item
}

// responseTools maps the tool specs to function tools. Strict mode is off,
// because it needs schemas that list every property as required, which
// source tools do not promise.
func responseTools(specs []model.ToolSpec) ([]responses.ToolUnionParam, error) {
	var out []responses.ToolUnionParam

	for _, s := range specs {
		params, err := toolParameters(s.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", s.Name, err)
		}

		t := &responses.FunctionToolParam{Name: s.Name, Parameters: params, Strict: openai.Bool(false)}
		if s.Description != "" {
			t.Description = openai.String(s.Description)
		}

		out = append(out, responses.ToolUnionParam{OfFunction: t})
	}

	return out, nil
}

// responseToolChoice maps the tool choice. An unset mode leaves it out.
func responseToolChoice(tc model.ToolChoice) responses.ResponseNewParamsToolChoiceUnion {
	mode := func(o responses.ToolChoiceOptions) responses.ResponseNewParamsToolChoiceUnion {
		return responses.ResponseNewParamsToolChoiceUnion{OfToolChoiceMode: openai.Opt(o)}
	}

	switch tc.Mode {
	case model.ToolChoiceAuto:
		return mode(responses.ToolChoiceOptionsAuto)
	case model.ToolChoiceAny:
		return mode(responses.ToolChoiceOptionsRequired)
	case model.ToolChoiceNone:
		return mode(responses.ToolChoiceOptionsNone)
	case model.ToolChoiceRequired:
		return responses.ResponseNewParamsToolChoiceUnion{OfFunctionTool: &responses.ToolChoiceFunctionParam{Name: tc.Name}}
	default:
		return responses.ResponseNewParamsToolChoiceUnion{}
	}
}

// fromResponse maps a finished response to the shared one. Reasoning items
// become Reasoning parts that keep the item ID and encrypted content, so
// they can be sent back unchanged. Item types the connector does not use
// are left out.
func fromResponse(r responses.Response, provider string) model.Response {
	var (
		parts   []model.Part
		calls   bool
		refused bool
	)

	for _, item := range r.Output {
		switch item.Type {
		case itemReasoning:
			parts = append(parts, model.Reasoning{Provider: provider, ID: item.ID, Text: summaryText(item.Summary), Signature: item.EncryptedContent})
		case itemMessage:
			for _, content := range item.Content {
				switch content.Type {
				case contentOutputText:
					parts = append(parts, model.Text{Text: content.Text})
				case contentRefusal:
					refused = true
				}
			}
		case itemFunctionCall:
			calls = true

			parts = append(parts, model.ToolCall{ID: item.CallID, Name: item.Name, Input: toolInput(item.Arguments.OfString)})
		}
	}

	if parts == nil {
		parts = []model.Part{}
	}

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: responseStop(r, calls, refused),
		Usage:      responseUsage(r.Usage),
		Model:      r.Model,
	}
}

// responseStop says why the model stopped.
func responseStop(r responses.Response, calls, refused bool) model.StopReason {
	if r.Status == responses.ResponseStatusIncomplete {
		switch r.IncompleteDetails.Reason {
		case incompleteMaxTokens:
			return model.StopMaxTokens
		case incompleteContentFilter:
			return model.StopRefusal
		default:
			return model.StopOther
		}
	}

	switch {
	case refused:
		return model.StopRefusal
	case calls:
		return model.StopToolUse
	default:
		return model.StopEndTurn
	}
}

// responseUsage maps usage. OpenAI counts cached and cache-written tokens
// inside input_tokens, so they are taken out to match model.Usage, where
// input tokens are the uncached ones.
func responseUsage(u responses.ResponseUsage) model.Usage {
	cached := u.InputTokensDetails.CachedTokens
	written := u.InputTokensDetails.CacheWriteTokens

	return model.Usage{
		InputTokens:      max(u.InputTokens-cached-written, 0),
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  cached,
		CacheWriteTokens: written,
		ReasoningTokens:  u.OutputTokensDetails.ReasoningTokens,
	}
}

// summaryText joins the parts of a reasoning summary.
func summaryText(summary []responses.ResponseReasoningItemSummary) string {
	texts := make([]string, 0, len(summary))
	for _, s := range summary {
		texts = append(texts, s.Text)
	}

	return strings.Join(texts, summarySeparator)
}

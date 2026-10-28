package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/model"
)

// Chat Completions finish reasons the connector maps.
const (
	finishStop          = "stop"
	finishToolCalls     = "tool_calls"
	finishLength        = "length"
	finishContentFilter = "content_filter"
)

// toolChoiceAuto, toolChoiceRequired and toolChoiceNone are the string
// tool choice modes of Chat Completions.
const (
	toolChoiceAuto     = "auto"
	toolChoiceRequired = "required"
	toolChoiceNone     = "none"
)

// callIDPrefix starts the IDs the connector gives tool calls that a
// server sent without one, so their results can still be matched.
const callIDPrefix = "call_"

// errChunkRejected is returned when a streamed chunk does not fit the
// ones before it, for example a tool call index out of order.
var errChunkRejected = errors.New("the server sent a chunk that does not fit the stream")

// errNoChoice is returned when a stream ended without a finished answer,
// for example because the connection dropped part way.
var errNoChoice = errors.New("stream ended without an answer")

// generateChat runs one turn on Chat Completions.
func (c *Connector) generateChat(ctx context.Context, req model.Request, opts []option.RequestOption) (model.Response, error) {
	params, err := c.chatParams(req)
	if err != nil {
		return model.Response{}, &buildError{err: err}
	}

	completion, requestID, err := c.streamChat(ctx, params, opts)
	if err != nil {
		return model.Response{}, fmt.Errorf("chat completion: %w", err)
	}

	out := fromChat(completion)
	out.RequestID = requestID

	return out, nil
}

// streamChat sends params as a streamed request and collects the chunks
// into one completion, with the request ID from the headers.
func (c *Connector) streamChat(ctx context.Context, params openai.ChatCompletionNewParams, opts []option.RequestOption) (completion openai.ChatCompletion, requestID string, err error) {
	var httpResp *http.Response

	s := c.chat.NewStreaming(ctx, params, append(opts, option.WithResponseInto(&httpResp))...)

	defer func() {
		if cerr := s.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	if httpResp != nil {
		requestID = requestIDOf(httpResp.Header)
	}

	var acc openai.ChatCompletionAccumulator

	for s.Next() {
		if !acc.AddChunk(s.Current()) {
			return completion, requestID, errChunkRejected
		}
	}

	if serr := s.Err(); serr != nil {
		return completion, requestID, fmt.Errorf("stream chat completion: %w", serr)
	}

	if len(acc.Choices) == 0 || acc.Choices[0].FinishReason == "" {
		return completion, requestID, errNoChoice
	}

	return acc.ChatCompletion, requestID, nil
}

// chatParams builds the Chat Completions request for req. Reasoning is not
// sent back: compatible servers have no way to take it.
func (c *Connector) chatParams(req model.Request) (openai.ChatCompletionNewParams, error) {
	messages, err := chatMessages(req.System, req.Messages)
	if err != nil {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("map messages: %w", err)
	}

	p := openai.ChatCompletionNewParams{
		Model:         c.model,
		Messages:      messages,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)},
	}

	// A server that ignores tool_choice would still let the model call a
	// tool it was told not to; leaving the tools out is the only sure way.
	if req.ToolChoice.Mode != model.ToolChoiceNone || c.caps.ToolChoice {
		tools, err := chatTools(req.Tools)
		if err != nil {
			return openai.ChatCompletionNewParams{}, fmt.Errorf("map tools: %w", err)
		}

		p.Tools = tools
	}

	if c.caps.ToolChoice {
		p.ToolChoice = chatToolChoice(req.ToolChoice)
	}

	if n := maxTokens(req.MaxTokens, c.settings.MaxTokens, c.caps); n > 0 {
		c.setMaxTokens(&p, int64(n))
	}

	if t := c.temperature(req); t != nil {
		p.Temperature = openai.Float(*t)
	}

	if effort := c.effort(req); effort != model.EffortDefault {
		p.ReasoningEffort = shared.ReasoningEffort(effort)
	}

	return p, nil
}

// setMaxTokens sets the output limit. OpenAI's own API wants
// max_completion_tokens; compatible servers widely take only max_tokens.
func (c *Connector) setMaxTokens(p *openai.ChatCompletionNewParams, n int64) {
	if c.maxCompletionTokens {
		p.MaxCompletionTokens = openai.Int(n)

		return
	}

	p.MaxTokens = openai.Int(n)
}

// chatMessages maps the system text and the conversation to messages. A
// user message's tool results become tool messages and its text a user
// message; an assistant message's text and tool calls become one
// assistant message. Reasoning parts are dropped.
func chatMessages(system []model.Text, msgs []model.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion

	if len(system) > 0 {
		out = append(out, openai.SystemMessage(systemText(system)))
	}

	for i, m := range msgs {
		switch m.Role {
		case model.User:
			out = append(out, userMessages(m)...)
		case model.Assistant:
			if msg, ok := assistantMessage(m); ok {
				out = append(out, msg)
			}
		default:
			return nil, fmt.Errorf("messages[%d]: %q: %w", i, m.Role, errUnknownRole)
		}
	}

	return out, nil
}

// userMessages maps one user message, keeping its parts in order.
func userMessages(m model.Message) []openai.ChatCompletionMessageParamUnion {
	var (
		out   []openai.ChatCompletionMessageParamUnion
		texts []string
	)

	flush := func() {
		if len(texts) > 0 {
			out = append(out, openai.UserMessage(strings.Join(texts, systemSeparator)))
			texts = nil
		}
	}

	for _, part := range m.Parts {
		switch p := part.(type) {
		case model.Text:
			texts = append(texts, p.Text)
		case model.ToolResult:
			flush()

			out = append(out, openai.ToolMessage(p.Content, p.CallID))
		}
	}

	flush()

	return out
}

// assistantMessage maps one assistant message, and false when nothing is
// left of it once reasoning is dropped.
func assistantMessage(m model.Message) (openai.ChatCompletionMessageParamUnion, bool) {
	var (
		texts []string
		calls []openai.ChatCompletionMessageToolCallUnionParam
	)

	for _, part := range m.Parts {
		switch p := part.(type) {
		case model.Text:
			texts = append(texts, p.Text)
		case model.ToolCall:
			calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:       p.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: p.Name, Arguments: toolArguments(p.Input)},
			}})
		}
	}

	if len(texts) == 0 && len(calls) == 0 {
		return openai.ChatCompletionMessageParamUnion{}, false
	}

	msg := &openai.ChatCompletionAssistantMessageParam{ToolCalls: calls}
	if len(texts) > 0 {
		msg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(strings.Join(texts, systemSeparator))}
	}

	return openai.ChatCompletionMessageParamUnion{OfAssistant: msg}, true
}

// chatTools maps the tool specs to function tools.
func chatTools(specs []model.ToolSpec) ([]openai.ChatCompletionToolUnionParam, error) {
	var out []openai.ChatCompletionToolUnionParam

	for _, s := range specs {
		params, err := toolParameters(s.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s: %w", s.Name, err)
		}

		def := shared.FunctionDefinitionParam{Name: s.Name, Parameters: params}
		if s.Description != "" {
			def.Description = openai.String(s.Description)
		}

		out = append(out, openai.ChatCompletionFunctionTool(def))
	}

	return out, nil
}

// chatToolChoice maps the tool choice. An unset mode leaves it out.
func chatToolChoice(tc model.ToolChoice) openai.ChatCompletionToolChoiceOptionUnionParam {
	mode := func(m string) openai.ChatCompletionToolChoiceOptionUnionParam {
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String(m)}
	}

	switch tc.Mode {
	case model.ToolChoiceAuto:
		return mode(toolChoiceAuto)
	case model.ToolChoiceAny:
		return mode(toolChoiceRequired)
	case model.ToolChoiceNone:
		return mode(toolChoiceNone)
	case model.ToolChoiceRequired:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
			Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: tc.Name},
		}}
	default:
		return openai.ChatCompletionToolChoiceOptionUnionParam{}
	}
}

// fromChat maps a collected completion to the shared response. A tool call
// the server sent without an ID gets one, so its result can be matched.
func fromChat(cc openai.ChatCompletion) model.Response {
	choice := cc.Choices[0]
	msg := choice.Message

	parts := []model.Part{}
	if msg.Content != "" {
		parts = append(parts, model.Text{Text: msg.Content})
	}

	for _, call := range msg.ToolCalls {
		id := call.ID
		if id == "" {
			id = callIDPrefix + ids.New().String()
		}

		parts = append(parts, model.ToolCall{ID: id, Name: call.Function.Name, Input: toolInput(call.Function.Arguments)})
	}

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: chatStop(choice.FinishReason, len(msg.ToolCalls) > 0, msg.Refusal != ""),
		Usage:      chatUsage(cc.Usage),
		Model:      cc.Model,
	}
}

// chatStop says why the model stopped. Some servers finish with stop even
// when the answer holds tool calls, so the calls decide.
func chatStop(finish string, calls, refused bool) model.StopReason {
	switch {
	case finish == finishLength:
		return model.StopMaxTokens
	case refused, finish == finishContentFilter:
		return model.StopRefusal
	case calls, finish == finishToolCalls:
		return model.StopToolUse
	case finish == finishStop:
		return model.StopEndTurn
	default:
		return model.StopOther
	}
}

// chatUsage maps usage. Cached and cache-written tokens are inside
// prompt_tokens, so they are taken out to match model.Usage.
func chatUsage(u openai.CompletionUsage) model.Usage {
	cached := u.PromptTokensDetails.CachedTokens
	written := u.PromptTokensDetails.CacheWriteTokens

	return model.Usage{
		InputTokens:      max(u.PromptTokens-cached-written, 0),
		OutputTokens:     u.CompletionTokens,
		CacheReadTokens:  cached,
		CacheWriteTokens: written,
		ReasoningTokens:  u.CompletionTokensDetails.ReasoningTokens,
	}
}

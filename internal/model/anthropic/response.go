package anthropic

import (
	anthropic "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Content block types of a response.
const (
	blockText             = "text"
	blockThinking         = "thinking"
	blockRedactedThinking = "redacted_thinking"
	blockToolUse          = "tool_use"
)

// stopReasons maps Anthropic stop reasons to the shared ones. Any other
// reason, such as stop_sequence or pause_turn, is model.StopOther.
var stopReasons = map[anthropic.StopReason]model.StopReason{
	anthropic.StopReasonEndTurn:   model.StopEndTurn,
	anthropic.StopReasonToolUse:   model.StopToolUse,
	anthropic.StopReasonMaxTokens: model.StopMaxTokens,
	// The context window filled up: the output was cut off as with
	// max_tokens.
	anthropic.StopReasonModelContextWindowExceeded: model.StopMaxTokens,
	anthropic.StopReasonRefusal:                    model.StopRefusal,
}

// response maps a complete message to the shared response. Thinking blocks
// become Reasoning parts that keep the text, signature and redacted data
// exactly, so they can be sent back in the next turn. Block types the
// connector does not use are left out.
func response(msg anthropic.Message) model.Response {
	parts := make([]model.Part, 0, len(msg.Content))

	for _, b := range msg.Content {
		switch b.Type {
		case blockText:
			parts = append(parts, model.Text{Text: b.Text})
		case blockThinking:
			parts = append(parts, model.Reasoning{Provider: Provider, Text: b.Thinking, Signature: b.Signature})
		case blockRedactedThinking:
			parts = append(parts, model.Reasoning{Provider: Provider, Redacted: []byte(b.Data)})
		case blockToolUse:
			parts = append(parts, model.ToolCall{ID: b.ID, Name: b.Name, Input: toolInput(b.Input)})
		}
	}

	stop, ok := stopReasons[msg.StopReason]
	if !ok {
		stop = model.StopOther
	}

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: stop,
		Usage: model.Usage{
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
			ReasoningTokens:  msg.Usage.OutputTokensDetails.ThinkingTokens,
		},
		Model: msg.Model,
	}
}

package bedrock

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/nourtalaat/sidekick/internal/model"
)

// stopReasons maps Converse stop reasons to the shared ones. Any other
// reason, such as stop_sequence or malformed_tool_use, is model.StopOther.
var stopReasons = map[types.StopReason]model.StopReason{
	types.StopReasonEndTurn:   model.StopEndTurn,
	types.StopReasonToolUse:   model.StopToolUse,
	types.StopReasonMaxTokens: model.StopMaxTokens,
	// The context window filled up: the output was cut off as with
	// max_tokens.
	types.StopReasonModelContextWindowExceeded: model.StopMaxTokens,
	// A guardrail or content filter stopped the answer: the provider
	// declined to answer.
	types.StopReasonGuardrailIntervened: model.StopRefusal,
	types.StopReasonContentFiltered:     model.StopRefusal,
}

// blockKind is the kind of one content block of an answer.
type blockKind int

// Block kinds the connector uses.
const (
	blockText blockKind = iota + 1
	blockReasoning
	blockRedacted
	blockToolUse
)

// block is one content block of an answer, from Converse or put together
// from ConverseStream events.
type block struct {
	kind      blockKind
	text      strings.Builder
	signature strings.Builder
	redacted  []byte
	id        string
	name      string
	input     strings.Builder
}

// turn is one complete answer.
type turn struct {
	blocks    []*block
	stop      types.StopReason
	usage     *types.TokenUsage
	requestID string
}

// accumulator collects ConverseStream events into blocks, by their
// content block index.
type accumulator struct {
	byIndex map[int32]*block
	stop    types.StopReason
	usage   *types.TokenUsage
}

// add takes one stream event. Events the connector does not use are
// ignored.
func (a *accumulator) add(event types.ConverseStreamOutput) {
	switch e := event.(type) {
	case *types.ConverseStreamOutputMemberContentBlockStart:
		if tu, ok := e.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
			b := a.block(e.Value.ContentBlockIndex, blockToolUse)
			b.id = aws.ToString(tu.Value.ToolUseId)
			b.name = aws.ToString(tu.Value.Name)
		}
	case *types.ConverseStreamOutputMemberContentBlockDelta:
		a.addDelta(aws.ToInt32(e.Value.ContentBlockIndex), e.Value.Delta)
	case *types.ConverseStreamOutputMemberMessageStop:
		a.stop = e.Value.StopReason
	case *types.ConverseStreamOutputMemberMetadata:
		a.usage = e.Value.Usage
	}
}

// addDelta adds one piece of a content block.
func (a *accumulator) addDelta(index int32, delta types.ContentBlockDelta) {
	switch d := delta.(type) {
	case *types.ContentBlockDeltaMemberText:
		a.block(&index, blockText).text.WriteString(d.Value)
	case *types.ContentBlockDeltaMemberToolUse:
		a.block(&index, blockToolUse).input.WriteString(aws.ToString(d.Value.Input))
	case *types.ContentBlockDeltaMemberReasoningContent:
		switch r := d.Value.(type) {
		case *types.ReasoningContentBlockDeltaMemberText:
			a.block(&index, blockReasoning).text.WriteString(r.Value)
		case *types.ReasoningContentBlockDeltaMemberSignature:
			a.block(&index, blockReasoning).signature.WriteString(r.Value)
		case *types.ReasoningContentBlockDeltaMemberRedactedContent:
			b := a.block(&index, blockRedacted)
			b.redacted = append(b.redacted, r.Value...)
		}
	}
}

// block returns the block at index, made with kind if it is new.
func (a *accumulator) block(index *int32, kind blockKind) *block {
	if a.byIndex == nil {
		a.byIndex = map[int32]*block{}
	}

	i := aws.ToInt32(index)

	b, ok := a.byIndex[i]
	if !ok {
		b = &block{kind: kind}
		a.byIndex[i] = b
	}

	return b
}

// blocks returns the blocks in index order.
func (a *accumulator) blocks() []*block {
	indexes := make([]int32, 0, len(a.byIndex))
	for i := range a.byIndex {
		indexes = append(indexes, i)
	}

	slices.Sort(indexes)

	out := make([]*block, 0, len(indexes))
	for _, i := range indexes {
		out = append(out, a.byIndex[i])
	}

	return out
}

// response maps a complete answer to the shared response. Reasoning blocks
// become Reasoning parts that keep the text, signature and redacted data
// exactly, so they can be sent back in the next turn. Block types the
// connector does not use are left out.
func response(t turn) model.Response {
	parts := make([]model.Part, 0, len(t.blocks))

	for _, b := range t.blocks {
		switch b.kind {
		case blockText:
			parts = append(parts, model.Text{Text: b.text.String()})
		case blockReasoning:
			parts = append(parts, model.Reasoning{Provider: Provider, Text: b.text.String(), Signature: b.signature.String()})
		case blockRedacted:
			parts = append(parts, model.Reasoning{Provider: Provider, Redacted: b.redacted})
		case blockToolUse:
			parts = append(parts, model.ToolCall{ID: b.id, Name: b.name, Input: toolInput(b.input.String())})
		}
	}

	stop, ok := stopReasons[t.stop]
	if !ok {
		stop = model.StopOther
	}

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: stop,
		Usage:      usage(t.usage),
		RequestID:  t.requestID,
	}
}

// blocksOf converts the content of a Converse answer to blocks.
func blocksOf(content []types.ContentBlock) ([]*block, error) {
	out := make([]*block, 0, len(content))

	for _, c := range content {
		switch v := c.(type) {
		case *types.ContentBlockMemberText:
			b := &block{kind: blockText}
			b.text.WriteString(v.Value)
			out = append(out, b)
		case *types.ContentBlockMemberToolUse:
			b := &block{kind: blockToolUse, id: aws.ToString(v.Value.ToolUseId), name: aws.ToString(v.Value.Name)}

			if v.Value.Input != nil {
				raw, err := v.Value.Input.MarshalSmithyDocument()
				if err != nil {
					return nil, fmt.Errorf("tool call %s: input: %w", b.id, err)
				}

				b.input.Write(raw)
			}

			out = append(out, b)
		case *types.ContentBlockMemberReasoningContent:
			if b := reasoningBlock(v.Value); b != nil {
				out = append(out, b)
			}
		}
	}

	return out, nil
}

// reasoningBlock converts one reasoning block of a Converse answer, or
// returns nil for a kind of reasoning the connector does not know.
func reasoningBlock(r types.ReasoningContentBlock) *block {
	switch v := r.(type) {
	case *types.ReasoningContentBlockMemberReasoningText:
		b := &block{kind: blockReasoning}
		b.text.WriteString(aws.ToString(v.Value.Text))
		b.signature.WriteString(aws.ToString(v.Value.Signature))

		return b
	case *types.ReasoningContentBlockMemberRedactedContent:
		return &block{kind: blockRedacted, redacted: v.Value}
	default:
		return nil
	}
}

// usage maps the token counts. Converse counts only uncached input in
// inputTokens, as model.Usage does.
func usage(u *types.TokenUsage) model.Usage {
	if u == nil {
		return model.Usage{}
	}

	return model.Usage{
		InputTokens:      int64(aws.ToInt32(u.InputTokens)),
		OutputTokens:     int64(aws.ToInt32(u.OutputTokens)),
		CacheReadTokens:  int64(aws.ToInt32(u.CacheReadInputTokens)),
		CacheWriteTokens: int64(aws.ToInt32(u.CacheWriteInputTokens)),
	}
}

// toolInput returns the input of a tool call as raw JSON, or an empty
// object when there is none.
func toolInput(in string) json.RawMessage {
	if in == "" {
		return emptyInput
	}

	return json.RawMessage(in)
}

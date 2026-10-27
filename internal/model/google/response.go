package google

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/model"
)

// generatedCallPrefix starts the IDs the connector gives tool calls that
// the API sent without one, so their results can still be matched. Such an
// ID is not sent back to the API.
const generatedCallPrefix = "sidekick-call-"

// refusals are the finish reasons that mean the model, or a filter in front
// of it, declined to answer.
var refusals = map[genai.FinishReason]bool{
	genai.FinishReasonSafety:                 true,
	genai.FinishReasonRecitation:             true,
	genai.FinishReasonLanguage:               true,
	genai.FinishReasonBlocklist:              true,
	genai.FinishReasonProhibitedContent:      true,
	genai.FinishReasonSPII:                   true,
	genai.FinishReasonImageSafety:            true,
	genai.FinishReasonImageProhibitedContent: true,
	genai.FinishReasonImageRecitation:        true,
}

// errIncomplete is returned when the stream ended before the answer did,
// for example because the connection dropped part way.
var errIncomplete = errors.New("stream ended before the answer was complete")

// errContinuation is returned when the API stopped the answer early and
// asked for a continuation request. Automatic continuation is off so that
// every request is retried the same way, so the whole call is retried.
var errContinuation = errors.New("the API stopped the answer early and asked for a continuation")

// accumulator collects the chunks of a streamed answer. Text that arrives
// over several chunks is joined into one part, and a thought signature
// that arrives in a chunk of its own joins the text before it.
type accumulator struct {
	parts      []*genai.Part
	finish     genai.FinishReason
	blocked    genai.BlockedReason
	usage      *genai.GenerateContentResponseUsageMetadata
	responseID string
}

// add takes one chunk.
func (a *accumulator) add(chunk *genai.GenerateContentResponse) {
	if chunk == nil {
		return
	}

	if chunk.ResponseID != "" {
		a.responseID = chunk.ResponseID
	}

	if chunk.UsageMetadata != nil {
		a.usage = chunk.UsageMetadata
	}

	if pf := chunk.PromptFeedback; pf != nil && pf.BlockReason != "" && pf.BlockReason != genai.BlockedReasonUnspecified {
		a.blocked = pf.BlockReason
	}

	if len(chunk.Candidates) == 0 || chunk.Candidates[0] == nil {
		return
	}

	cand := chunk.Candidates[0]
	if cand.FinishReason != "" && cand.FinishReason != genai.FinishReasonUnspecified {
		a.finish = cand.FinishReason
	}

	if cand.Content == nil {
		return
	}

	for _, p := range cand.Content.Parts {
		a.addPart(p)
	}
}

// addPart adds one part of a chunk. Parts other than text and function
// calls, such as code execution, are not used.
func (a *accumulator) addPart(p *genai.Part) {
	switch {
	case p == nil:
	case p.FunctionCall != nil:
		a.parts = append(a.parts, &genai.Part{FunctionCall: p.FunctionCall, ThoughtSignature: p.ThoughtSignature})
	case p.Text != "" || len(p.ThoughtSignature) > 0:
		if last := a.last(); last != nil && last.FunctionCall == nil && last.Thought == p.Thought && len(last.ThoughtSignature) == 0 {
			last.Text += p.Text
			last.ThoughtSignature = p.ThoughtSignature

			return
		}

		a.parts = append(a.parts, &genai.Part{Text: p.Text, Thought: p.Thought, ThoughtSignature: p.ThoughtSignature})
	}
}

// last returns the last part so far, or nil.
func (a *accumulator) last() *genai.Part {
	if len(a.parts) == 0 {
		return nil
	}

	return a.parts[len(a.parts)-1]
}

// response maps the collected answer to the shared response. Thought
// signatures become Reasoning parts in front of the part they came in, so
// they can be sent back in the next turn.
func (a *accumulator) response() (model.Response, error) {
	if a.blocked != "" {
		return model.Response{
			Message:    model.Message{Role: model.Assistant},
			StopReason: model.StopRefusal,
			Usage:      usage(a.usage),
			RequestID:  a.responseID,
		}, nil
	}

	switch a.finish {
	case "":
		return model.Response{}, errIncomplete
	case genai.FinishReasonContinuation:
		return model.Response{}, errContinuation
	}

	parts := make([]model.Part, 0, len(a.parts))
	calls := false

	for _, p := range a.parts {
		sig := base64.StdEncoding.EncodeToString(p.ThoughtSignature)

		switch {
		case p.FunctionCall != nil:
			call, err := toolCall(p.FunctionCall)
			if err != nil {
				return model.Response{}, fmt.Errorf("map answer: %w", err)
			}

			if len(p.ThoughtSignature) > 0 {
				parts = append(parts, model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: sig})
			}

			parts = append(parts, call)
			calls = true
		case p.Thought:
			parts = append(parts, model.Reasoning{Provider: Provider, ID: reasoningThought, Text: p.Text, Signature: sig})
		case p.Text == "":
			parts = append(parts, model.Reasoning{Provider: Provider, ID: reasoningPart, Signature: sig})
		default:
			if len(p.ThoughtSignature) > 0 {
				parts = append(parts, model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: sig})
			}

			parts = append(parts, model.Text{Text: p.Text})
		}
	}

	return model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: stopReason(a.finish, calls),
		Usage:      usage(a.usage),
		RequestID:  a.responseID,
	}, nil
}

// toolCall maps a function call. A call without an ID gets one, so its
// result can be matched to it.
func toolCall(fc *genai.FunctionCall) (model.ToolCall, error) {
	input := emptyArgs

	if len(fc.Args) > 0 {
		raw, err := json.Marshal(fc.Args)
		if err != nil {
			return model.ToolCall{}, fmt.Errorf("tool call %s: input: %w", fc.Name, err)
		}

		input = raw
	}

	id := fc.ID
	if id == "" {
		id = generatedCallPrefix + ids.New().String()
	}

	return model.ToolCall{ID: id, Name: fc.Name, Input: input}, nil
}

// stopReason maps a finish reason. The API finishes a turn that calls
// tools with STOP, so calls says whether it did.
func stopReason(finish genai.FinishReason, calls bool) model.StopReason {
	switch {
	case finish == genai.FinishReasonStop && calls:
		return model.StopToolUse
	case finish == genai.FinishReasonStop:
		return model.StopEndTurn
	case finish == genai.FinishReasonMaxTokens:
		return model.StopMaxTokens
	case refusals[finish]:
		return model.StopRefusal
	default:
		return model.StopOther
	}
}

// usage maps the token counts. The prompt count includes cached tokens and
// the output count leaves out thinking, while model.Usage counts uncached
// input and bills thinking as output.
func usage(u *genai.GenerateContentResponseUsageMetadata) model.Usage {
	if u == nil {
		return model.Usage{}
	}

	return model.Usage{
		InputTokens:     int64(u.PromptTokenCount) + int64(u.ToolUsePromptTokenCount) - int64(u.CachedContentTokenCount),
		OutputTokens:    int64(u.CandidatesTokenCount) + int64(u.ThoughtsTokenCount),
		CacheReadTokens: int64(u.CachedContentTokenCount),
		ReasoningTokens: int64(u.ThoughtsTokenCount),
	}
}

// apiCallID returns the ID to send the API for a tool call: its own, or
// none for an ID the connector made up.
func apiCallID(id string) string {
	if strings.HasPrefix(id, generatedCallPrefix) {
		return ""
	}

	return id
}

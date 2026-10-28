package investigator

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The values of steps.kind.
const (
	// kindPrompt is the first step: the system text, the tools and the
	// first user message.
	kindPrompt = "prompt"
	// kindModelTurn is one answer of the model.
	kindModelTurn = "model_turn"
	// kindToolResult answers one tool call of the model turn before it.
	kindToolResult = "tool_result"
	// kindNote is text Sidekick adds to the conversation, such as a
	// reminder to submit a verdict.
	kindNote = "note"
	// kindInbox delivers what arrived while the investigation ran, such as
	// new events in its group, recoveries and follow-up questions.
	kindInbox = "inbox"
	// kindSummary replaces the content of an earlier tool result in the
	// conversation with a short summary, when the conversation grows too
	// long for the model. Its parts hold the tool result as it is sent
	// now; the earlier step keeps the original.
	kindSummary = "summary"
	// kindTriagePrompt is the request of triage, kindTriageTurn the triage
	// model's answer, and kindTriageResult what Sidekick made of it: a full
	// investigation with a strategy, or a triage-only result. They come
	// before the prompt and are not part of the investigation's
	// conversation.
	kindTriagePrompt = "triage_prompt"
	kindTriageTurn   = "triage_turn"
	kindTriageResult = "triage_result"
	// kindTriageOverride records that a responder asked for a full
	// investigation after a triage-only result.
	kindTriageOverride = "triage_override"
)

// The values of steps.status.
const (
	statusOK          = "ok"
	statusNoData      = "no_data"
	statusError       = "error"
	statusTimeout     = "timeout"
	statusUnavailable = "unavailable"
	// statusRejected is a call that Sidekick refused without running it:
	// input that does not match the tool's schema, an unknown tool, or a
	// report or finding that did not pass its checks.
	statusRejected = "rejected"
	// statusOverBudget is a source call that was not run because the
	// investigation had used all of its tool calls.
	statusOverBudget = "over_budget"
)

// Part types in steps.parts.
const (
	partText       = "text"
	partToolCall   = "tool_call"
	partToolResult = "tool_result"
	partReasoning  = "reasoning"
)

var (
	errUnknownPart = errors.New("unknown part type")
	errNoPrompt    = errors.New("the first step is not the prompt")
)

// storedPart is one model.Part as steps.parts holds it.
type storedPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Cache     bool            `json:"cache,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Redacted  []byte          `json:"redacted,omitempty"`
}

// storedPrompt is the parts of a prompt step: what every request of the
// run starts with. Keeping it in the step keeps the start of the
// conversation the same on every resume, even when the config changes.
type storedPrompt struct {
	System []storedPart `json:"system"`
	Tools  []storedTool `json:"tools"`
	Parts  []storedPart `json:"parts"`
}

// storedTool is one model.ToolSpec of a prompt.
type storedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	Cache       bool            `json:"cache,omitempty"`
}

// history is the saved steps of an investigation and what they add up to:
// the conversation to send the model, and indexes of the tool calls and
// their results. Only the goroutine that runs the loop uses it.
type history struct {
	steps []db.Step
	// req holds the system text, the tools and the messages so far.
	req model.Request
	// calls are the tool calls of every model turn, by ID.
	calls map[string]model.ToolCall
	// results are the tool result steps, by the ID of the call they answer.
	results map[string]db.Step
	// lastTurn is the index in steps of the last model turn, or -1, and
	// turnMessage the index of its message in req.Messages.
	lastTurn    int
	turnMessage int
	// finalNote is the index in steps of the note that starts the final
	// turn, or -1, and finalBudget the budget it names.
	finalNote   int
	finalBudget string
	// run is the run of the last step, or the run startRun began, and
	// runFrom the index in steps of its first step. The checks of how a
	// run goes, such as its reminders, its final turn and its tokens, look
	// only at the steps from runFrom on.
	run     int32
	runFrom int
	// resultAt is where each tool result is in req.Messages, by call ID:
	// the message and the part. summarized holds the call IDs whose result
	// a summary step replaced.
	resultAt   map[string][2]int
	summarized map[string]bool
	// prompt is the index in steps of the prompt, or -1 before it.
	prompt int
	// triage holds the triage steps.
	triage triageSteps
}

// startRun makes run the current run, when it is newer than the run of
// the last step: the steps saved from now on are its steps.
func (h *history) startRun(run int32) {
	if run <= h.run {
		return
	}

	h.run, h.runFrom = run, len(h.steps)
	h.finalNote, h.finalBudget = -1, ""
}

// runSteps returns the steps of the current run.
func (h *history) runSteps() []db.Step {
	return h.steps[h.runFrom:]
}

// runUse returns when the current run started, which is when its first
// step started or, before it has one, now, and what the runs before it
// used of inv's counters.
func (h *history) runUse(now time.Time, inv db.Investigation) (time.Time, usage) {
	before := usage{costMicros: inv.CostMicros, toolCalls: inv.ToolCalls}

	for _, s := range h.runSteps() {
		before.costMicros -= deref(s.CostMicros)

		if countsAsToolCall(s) {
			before.toolCalls--
		}
	}

	if steps := h.runSteps(); len(steps) > 0 {
		return steps[0].StartedAt, before
	}

	return now, before
}

// add appends a saved step and adds it to the conversation.
func (h *history) add(s db.Step) error {
	h.startRun(s.Run)

	switch s.Kind {
	case kindTriagePrompt, kindTriageTurn, kindTriageResult, kindTriageOverride:
		if h.prompt >= 0 {
			return fmt.Errorf("step %d: a triage step after the prompt", s.Seq)
		}

		if err := h.triage.add(len(h.steps), s); err != nil {
			return fmt.Errorf("step %d: %w", s.Seq, err)
		}
	case kindPrompt:
		if h.prompt >= 0 {
			return fmt.Errorf("step %d: a second prompt", s.Seq)
		}

		var p storedPrompt
		if err := json.Unmarshal(s.Parts, &p); err != nil {
			return fmt.Errorf("decode prompt step %d: %w", s.Seq, err)
		}

		req, err := p.request()
		if err != nil {
			return fmt.Errorf("decode prompt step %d: %w", s.Seq, err)
		}

		h.req = req
		h.prompt = len(h.steps)
	default:
		if h.prompt < 0 {
			return errNoPrompt
		}

		parts, err := decodeParts(s.Parts)
		if err != nil {
			return fmt.Errorf("decode step %d: %w", s.Seq, err)
		}

		if s.Kind == kindSummary {
			if err := h.replaceResult(parts); err != nil {
				return fmt.Errorf("step %d: %w", s.Seq, err)
			}
		} else {
			h.addParts(s, parts)
		}
	}

	if s.Kind == kindNote && s.Budget != nil && h.finalNote < 0 {
		h.finalNote, h.finalBudget = len(h.steps), *s.Budget
	}

	h.steps = append(h.steps, s)

	return nil
}

// addParts adds the parts of a step after the prompt to the conversation
// and the indexes. A model turn is an assistant message; tool results and
// notes join the user message after it.
func (h *history) addParts(s db.Step, parts []model.Part) {
	if s.Kind == kindModelTurn {
		h.req.Messages = append(h.req.Messages, model.Message{Role: model.Assistant, Parts: parts})
		h.lastTurn = len(h.steps)
		h.turnMessage = len(h.req.Messages) - 1

		for _, p := range parts {
			if call, ok := p.(model.ToolCall); ok {
				h.calls[call.ID] = call
			}
		}

		return
	}

	if s.Kind == kindToolResult && s.ToolCallID != nil {
		h.results[*s.ToolCallID] = s
	}

	last := len(h.req.Messages) - 1
	if last < 0 || h.req.Messages[last].Role != model.User {
		h.req.Messages = append(h.req.Messages, model.Message{Role: model.User})
		last++
	}

	for _, p := range parts {
		if r, ok := p.(model.ToolResult); ok {
			h.resultAt[r.CallID] = [2]int{last, len(h.req.Messages[last].Parts)}
		}

		h.req.Messages[last].Parts = append(h.req.Messages[last].Parts, p)
	}
}

// replaceResult puts the tool result of a summary step in place of the
// result it summarizes.
func (h *history) replaceResult(parts []model.Part) error {
	if len(parts) != 1 {
		return fmt.Errorf("a summary has %d parts, not 1", len(parts))
	}

	r, ok := parts[0].(model.ToolResult)
	if !ok {
		return errors.New("a summary that is not a tool result")
	}

	at, ok := h.resultAt[r.CallID]
	if !ok {
		return fmt.Errorf("a summary of call %q, which has no result", r.CallID)
	}

	h.req.Messages[at[0]].Parts[at[1]] = r
	h.summarized[r.CallID] = true

	return nil
}

// request returns the conversation to send the model next. The caller
// adds the metadata; the slices are copies.
func (h *history) request() model.Request {
	req := h.req
	req.Messages = make([]model.Message, len(h.req.Messages))

	for i, m := range h.req.Messages {
		req.Messages[i] = model.Message{Role: m.Role, Parts: append([]model.Part(nil), m.Parts...)}
	}

	return req
}

// unanswered returns the tool calls of the last model turn that have no
// result yet, in call order. After a resume these are the calls that were
// in flight when the last worker stopped.
func (h *history) unanswered() []model.ToolCall {
	var out []model.ToolCall

	for _, call := range h.turnCalls() {
		if _, ok := h.results[call.ID]; !ok {
			out = append(out, call)
		}
	}

	return out
}

// turnCalls returns the tool calls of the last model turn, in order.
func (h *history) turnCalls() []model.ToolCall {
	if h.lastTurn < 0 {
		return nil
	}

	var out []model.ToolCall

	for _, p := range h.req.Messages[h.turnMessage].Parts {
		if call, ok := p.(model.ToolCall); ok {
			out = append(out, call)
		}
	}

	return out
}

// needsReminder reports whether the last step is a model turn of the
// current run that called no tool, so the model ended its turn without a
// verdict or an answer.
func (h *history) needsReminder() bool {
	return h.lastTurn >= h.runFrom && h.lastTurn == len(h.steps)-1 && len(h.turnCalls()) == 0
}

// finalTurn returns the budget of the final turn, when it has started.
func (h *history) finalTurn() (budget string, started bool) {
	return h.finalBudget, h.finalNote >= 0
}

// finalTurnTaken reports whether the model has answered in the final
// turn.
func (h *history) finalTurnTaken() bool {
	return h.finalNote >= 0 && h.lastTurn > h.finalNote
}

// prompted reports whether the prompt of the investigation is saved.
func (h *history) prompted() bool {
	return h.prompt >= 0
}

// tokens returns the tokens of every model call of the current run, model
// turns, summaries and triage: input, including those read from or written
// to the prompt cache, and output.
func (h *history) tokens() int64 {
	var n int64

	for _, s := range h.runSteps() {
		if s.Kind != kindModelTurn && s.Kind != kindSummary && s.Kind != kindTriageTurn {
			continue
		}

		n += deref(s.InputTokens) + deref(s.CacheReadTokens) + deref(s.CacheWriteTokens) + deref(s.OutputTokens)
	}

	return n
}

// count returns the number of steps of the current run for which match
// is true.
func (h *history) count(match func(db.Step) bool) int {
	n := 0

	for _, s := range h.runSteps() {
		if match(s) {
			n++
		}
	}

	return n
}

// newHistory returns the history of the saved steps, in seq order.
func newHistory(steps []db.Step) (*history, error) {
	h := &history{
		calls:      map[string]model.ToolCall{},
		results:    map[string]db.Step{},
		resultAt:   map[string][2]int{},
		summarized: map[string]bool{},
		lastTurn:   -1,
		finalNote:  -1,
		prompt:     -1,
		triage:     triageSteps{prompt: -1, turn: -1, result: -1, override: -1},
	}

	for _, s := range steps {
		if int(s.Seq) != len(h.steps) {
			return nil, fmt.Errorf("step %d is missing", len(h.steps))
		}

		if err := h.add(s); err != nil {
			return nil, fmt.Errorf("add step %d: %w", s.Seq, err)
		}
	}

	return h, nil
}

// request returns the prompt as the start of a model request.
func (p storedPrompt) request() (model.Request, error) {
	system, err := decodeStoredParts(p.System)
	if err != nil {
		return model.Request{}, fmt.Errorf("system: %w", err)
	}

	parts, err := decodeStoredParts(p.Parts)
	if err != nil {
		return model.Request{}, fmt.Errorf("first message: %w", err)
	}

	req := model.Request{Messages: []model.Message{{Role: model.User, Parts: parts}}}

	for _, part := range system {
		text, ok := part.(model.Text)
		if !ok {
			return model.Request{}, errors.New("system: a part that is not text")
		}

		req.System = append(req.System, text)
	}

	for _, t := range p.Tools {
		req.Tools = append(req.Tools, model.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, Cache: t.Cache})
	}

	return req, nil
}

// encodePrompt returns the parts of the prompt step of req.
func encodePrompt(req model.Request) (json.RawMessage, error) {
	p := storedPrompt{Tools: []storedTool{}}

	for _, t := range req.System {
		p.System = append(p.System, storedPart{Type: partText, Text: t.Text, Cache: t.Cache})
	}

	for _, t := range req.Tools {
		p.Tools = append(p.Tools, storedTool{Name: t.Name, Description: t.Description, InputSchema: validJSON(t.InputSchema), Cache: t.Cache})
	}

	if len(req.Messages) > 0 {
		p.Parts = storedParts(req.Messages[0].Parts)
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode the prompt: %w", err)
	}

	return store.JSON(raw), nil
}

// encodeParts returns parts as steps.parts holds them.
func encodeParts(parts []model.Part) (json.RawMessage, error) {
	raw, err := json.Marshal(storedParts(parts))
	if err != nil {
		return nil, fmt.Errorf("encode the parts: %w", err)
	}

	return store.JSON(raw), nil
}

// storedParts converts parts for storage.
func storedParts(parts []model.Part) []storedPart {
	out := make([]storedPart, 0, len(parts))

	for _, part := range parts {
		switch p := part.(type) {
		case model.Text:
			out = append(out, storedPart{Type: partText, Text: p.Text, Cache: p.Cache})
		case model.ToolCall:
			out = append(out, storedPart{Type: partToolCall, ID: p.ID, Name: p.Name, Input: validJSON(p.Input)})
		case model.ToolResult:
			out = append(out, storedPart{Type: partToolResult, CallID: p.CallID, Content: p.Content, IsError: p.IsError})
		case model.Reasoning:
			out = append(out, storedPart{Type: partReasoning, Provider: p.Provider, ID: p.ID, Text: p.Text, Signature: p.Signature, Redacted: p.Redacted})
		}
	}

	return out
}

// decodeParts reads steps.parts.
func decodeParts(raw json.RawMessage) ([]model.Part, error) {
	var stored []storedPart
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode parts: %w", err)
	}

	return decodeStoredParts(stored)
}

// decodeStoredParts converts stored parts back to model parts.
func decodeStoredParts(stored []storedPart) ([]model.Part, error) {
	parts := make([]model.Part, 0, len(stored))

	for _, p := range stored {
		switch p.Type {
		case partText:
			parts = append(parts, model.Text{Text: p.Text, Cache: p.Cache})
		case partToolCall:
			parts = append(parts, model.ToolCall{ID: p.ID, Name: p.Name, Input: p.Input})
		case partToolResult:
			parts = append(parts, model.ToolResult{CallID: p.CallID, Content: p.Content, IsError: p.IsError})
		case partReasoning:
			parts = append(parts, model.Reasoning{Provider: p.Provider, ID: p.ID, Text: p.Text, Signature: p.Signature, Redacted: p.Redacted})
		default:
			return nil, fmt.Errorf("%w %q", errUnknownPart, p.Type)
		}
	}

	return parts, nil
}

// validJSON returns raw, or the empty object when raw is not valid JSON,
// which a jsonb column could not hold. A model can send a tool call whose
// input does not parse; the guard has answered it with an error already.
func validJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || json.Valid(raw) {
		return raw
	}

	return json.RawMessage(`{}`)
}

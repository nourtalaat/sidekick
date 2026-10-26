// Package modeltest has a model connector for tests that replays scripted
// turns, so code that runs a conversation, such as the agent loop, can be
// tested turn by turn without a provider.
package modeltest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/nourtalaat/sidekick/internal/model"
)

// errNoTurnLeft is returned by Generate once every scripted turn is used.
var errNoTurnLeft = errors.New("modeltest: no scripted turn left")

// Turn is one scripted answer to Generate: Response and Err, or what Func
// returns when it is set.
type Turn struct {
	Response model.Response
	Err      error
	// Func, when set, answers the call instead, for example to wait until
	// the context ends.
	Func func(ctx context.Context, req model.Request) (model.Response, error)
}

// Connector is a model.Connector that answers each Generate call with the
// next scripted turn, in order, and records every request. A call after the
// last turn fails the test. It is safe for concurrent use.
type Connector struct {
	t    testing.TB
	caps model.Capabilities

	mu       sync.Mutex
	turns    []Turn
	requests []model.Request
}

var _ model.Connector = (*Connector)(nil)

// Capabilities returns the capabilities New was given.
func (c *Connector) Capabilities() model.Capabilities {
	return c.caps
}

// Generate records req and answers with the next turn.
func (c *Connector) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, cloneRequest(req))

	if len(c.turns) == 0 {
		c.mu.Unlock()
		c.t.Errorf("modeltest: Generate called after the last of the scripted turns")

		return model.Response{}, errNoTurnLeft
	}

	turn := c.turns[0]
	c.turns = c.turns[1:]
	c.mu.Unlock()

	if turn.Func != nil {
		return turn.Func(ctx, req)
	}

	return turn.Response, turn.Err
}

// Requests returns every request received so far, oldest first.
func (c *Connector) Requests() []model.Request {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.requests)
}

// ExpectAllUsed fails the test, when it ends, if a scripted turn was not
// used.
func (c *Connector) ExpectAllUsed() {
	c.t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		if len(c.turns) > 0 {
			c.t.Errorf("modeltest: %d scripted turns were not used", len(c.turns))
		}
	})
}

// New returns a connector with caps that answers with turns.
func New(t testing.TB, caps model.Capabilities, turns ...Turn) *Connector {
	t.Helper()

	return &Connector{t: t, caps: caps, turns: slices.Clone(turns)}
}

// ToolUse returns a turn in which the model calls tools, with usage.
func ToolUse(usage model.Usage, calls ...model.ToolCall) Turn {
	parts := make([]model.Part, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, call)
	}

	return Turn{Response: model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: parts},
		StopReason: model.StopToolUse,
		Usage:      usage,
	}}
}

// Text returns a turn in which the model answers with text and calls no
// tool, with usage.
func Text(usage model.Usage, text string) Turn {
	return Turn{Response: model.Response{
		Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: text}}},
		StopReason: model.StopEndTurn,
		Usage:      usage,
	}}
}

// Fail returns a turn that fails with err.
func Fail(err error) Turn {
	return Turn{Err: err}
}

// Call returns a tool call with a JSON input.
func Call(id, name, input string) model.ToolCall {
	return model.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

// cloneRequest copies the slices of req that a caller might change after
// the call, so the recorded request stays as it was sent.
func cloneRequest(req model.Request) model.Request {
	req.System = slices.Clone(req.System)
	req.Tools = slices.Clone(req.Tools)
	req.Messages = slices.Clone(req.Messages)

	for i, m := range req.Messages {
		req.Messages[i].Parts = slices.Clone(m.Parts)
	}

	return req
}

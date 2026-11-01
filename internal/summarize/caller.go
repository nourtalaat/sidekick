// Package summarize makes the model calls of the summarize role: short
// jobs that ask the model to answer through one tool, such as drafting a
// lesson from feedback. Each call is priced and added to the day's spend of
// the route it belongs to, and a call is refused once a daily cap is
// reached, so these calls count in the caps like any other model call.
package summarize

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

var (
	// ErrOverCap is returned by CallTool, without calling the model, when
	// the route's daily cap or the global one is reached.
	ErrOverCap = errors.New("a daily cap is reached")
	// ErrDisabled is returned by CallTool when there is no summarize
	// connector.
	ErrDisabled = errors.New("no summarize role is configured")
	// ErrNoToolCall is returned by CallTool when the model answered
	// without calling the tool. The call is priced all the same.
	ErrNoToolCall = errors.New("the model did not call the tool")
)

// requireToolText is added to the system text when the connector cannot be
// forced to call a tool, to ask for the call instead.
const requireToolText = "\n\nAnswer by calling the %s tool exactly once. Do not answer with plain text."

// Daily holds the daily spending caps. They have the meaning of the caps of
// the investigator: the day starts at midnight in Location, and a cap of
// zero or less is not set.
type Daily struct {
	// Location is the time zone whose midnight starts a new day. Nil means
	// UTC.
	Location *time.Location
	// MaxCostMicros is the global cap, in micro-dollars: what every call
	// together may cost in one day.
	MaxCostMicros int64
	// RouteMicros are the caps of single routes, by route name.
	RouteMicros map[string]int64
}

// Options set up a Caller.
type Options struct {
	// Connector is the connector of the summarize role. Wrap it with
	// instrument.Wrap first. Nil turns the Caller off.
	Connector model.Connector
	// Name is the connector's name in the config, which spend_daily records.
	Name string
	// Model is the model ID the calls are priced as. Empty means the model
	// the response names.
	Model string
	// Retries is how many times a call that failed with a retryable error is
	// repeated.
	Retries int
	// Prices price each call.
	Prices pricing.Table
	// Daily are the daily spending caps.
	Daily Daily
	// Logger gets a line when a call is refused or does not use the tool.
	// Nil logs nothing.
	Logger *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Request is one call of CallTool.
type Request struct {
	// Route is the route the call belongs to, whose daily spend it joins.
	Route string
	// System is the system text.
	System string
	// Messages are the conversation. Everything in them that came from
	// people, events or tools should be wrapped as untrusted data first.
	Messages []model.Message
	// Tool is the one tool the model must call.
	Tool model.ToolSpec
	// MaxTokens limits the answer. Zero leaves it to the connector.
	MaxTokens int
}

// Caller makes the calls of the summarize role. It is safe for concurrent
// use.
type Caller struct {
	connector model.Connector
	name      string
	model     string
	retries   int
	prices    pricing.Table
	daily     Daily
	log       *slog.Logger
	now       func() time.Time
}

// New returns a Caller. Without a connector it is off: Enabled is false and
// CallTool returns ErrDisabled.
func New(opts Options) *Caller {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Caller{
		connector: opts.Connector,
		name:      opts.Name,
		model:     opts.Model,
		retries:   opts.Retries,
		prices:    opts.Prices,
		daily:     opts.Daily,
		log:       log,
		now:       now,
	}
}

// Enabled reports whether there is a summarize connector.
func (c *Caller) Enabled() bool {
	return c != nil && c.connector != nil
}

// CallTool asks the model to answer through req.Tool and returns the input
// of its call. It forces the tool where the connector allows it, and asks
// for it in the system text where not. The call is priced and added to the
// day's spend of req.Route through q, also when the model does not call the
// tool. Pass the pool to save the spend at once, or a transaction to save it
// with the caller's own writes; a transaction stays open during the model
// call, which can take seconds.
//
// It returns ErrDisabled for a Caller that is off, ErrOverCap when a daily
// cap is reached, ErrNoToolCall when the model calls no tool, and the
// connector's error, wrapped, when the call fails.
func (c *Caller) CallTool(ctx context.Context, q db.DBTX, req Request) (json.RawMessage, error) {
	return c.callTool(ctx, queryLedger{q: db.New(q)}, req)
}

// NextDay returns the start of the next day in the caps' time zone, when a
// call refused with ErrOverCap can be tried again.
func (c *Caller) NextDay() time.Time {
	loc := c.location()
	y, m, d := c.now().In(loc).Date()

	return time.Date(y, m, d, 0, 0, 0, 0, loc).AddDate(0, 0, 1)
}

// callTool is CallTool over a ledger.
func (c *Caller) callTool(ctx context.Context, l ledger, req Request) (json.RawMessage, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	if err := c.checkCap(ctx, l, req.Route); err != nil {
		return nil, fmt.Errorf("route %s: %w", req.Route, err)
	}

	resp, err := model.Generate(ctx, c.connector, c.request(req), c.retries)
	if err != nil {
		return nil, fmt.Errorf("call the summarize model: %w", err)
	}

	if err := c.addSpend(ctx, l, req.Route, resp); err != nil {
		return nil, fmt.Errorf("route %s: %w", req.Route, err)
	}

	input, ok := toolInput(resp.Message, req.Tool.Name)
	if !ok {
		c.log.WarnContext(ctx, "the summarize model did not call the tool", "tool", req.Tool.Name, "route", req.Route,
			"stop_reason", resp.StopReason)

		return nil, fmt.Errorf("tool %s: %w", req.Tool.Name, ErrNoToolCall)
	}

	return input, nil
}

// request builds the model request of req.
func (c *Caller) request(req Request) model.Request {
	system := req.System
	choice := model.ToolChoice{Mode: model.ToolChoiceRequired, Name: req.Tool.Name}

	if !c.connector.Capabilities().ToolChoice {
		system += fmt.Sprintf(requireToolText, req.Tool.Name)
		choice = model.ToolChoice{}
	}

	return model.Request{
		System:     []model.Text{{Text: system}},
		Messages:   req.Messages,
		Tools:      []model.ToolSpec{req.Tool},
		ToolChoice: choice,
		MaxTokens:  req.MaxTokens,
		Metadata:   map[string]string{model.MetadataRole: string(model.RoleSummarize), model.MetadataRoute: req.Route},
	}
}

// checkCap returns an error that wraps ErrOverCap when the route's daily
// cap or the global one is reached.
func (c *Caller) checkCap(ctx context.Context, l ledger, route string) error {
	routeCap, global := c.daily.RouteMicros[route], c.daily.MaxCostMicros
	if routeCap <= 0 && global <= 0 {
		return nil
	}

	spent, err := l.dailySpend(ctx, route, c.today())
	if err != nil {
		return fmt.Errorf("read the daily spend: %w", err)
	}

	switch {
	case routeCap > 0 && spent.route >= routeCap:
		return fmt.Errorf("the route has spent %d of its %d micro-dollars today: %w", spent.route, routeCap, ErrOverCap)
	case global > 0 && spent.total >= global:
		return fmt.Errorf("all routes have spent %d of the %d micro-dollars allowed today: %w", spent.total, global, ErrOverCap)
	default:
		return nil
	}
}

// addSpend prices the response and adds it to the day's spend of route.
func (c *Caller) addSpend(ctx context.Context, l ledger, route string, resp model.Response) error {
	modelID := cmp.Or(c.model, resp.Model)

	var cost int64

	if price, ok := c.prices.Lookup(modelID); ok {
		cost = price.Cost(resp.Usage)
	}

	err := l.addSpend(ctx, db.AddSpendParams{
		Day:          c.today(),
		Route:        route,
		Connector:    c.name,
		Model:        modelID,
		CostMicros:   cost,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	})
	if err != nil {
		return fmt.Errorf("add the call to the daily spend: %w", err)
	}

	return nil
}

// today returns the day it is now in the caps' time zone, as spend_daily
// stores it.
func (c *Caller) today() pgtype.Date {
	y, m, d := c.now().In(c.location()).Date()

	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

// location returns the caps' time zone.
func (c *Caller) location() *time.Location {
	if c.daily.Location == nil {
		return time.UTC
	}

	return c.daily.Location
}

// toolInput returns the input of the first call to the tool called name in
// msg.
func toolInput(msg model.Message, name string) (json.RawMessage, bool) {
	for _, p := range msg.Parts {
		if call, ok := p.(model.ToolCall); ok && call.Name == name {
			return call.Input, true
		}
	}

	return nil, false
}

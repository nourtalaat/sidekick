// Package summarize makes the model calls of features that use the
// summarize role, such as drafting a benchmark scenario's rubric. A call
// forces one tool, so the answer is JSON that follows a schema, is priced,
// and is added to the day's spend of the route in the caller's
// transaction. A call is refused when the daily cap is reached.
package summarize

import (
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
	// ErrDisabled is returned when no summarize connector is configured.
	ErrDisabled = errors.New("no summarize model is configured")
	// ErrOverCap is returned, without a model call, when the global or the
	// route's daily spending cap is reached.
	ErrOverCap = errors.New("the daily spending cap is reached")
	// ErrNoToolCall is returned when the model answered without calling the
	// tool it was told to call. The call is still counted in the spend.
	ErrNoToolCall = errors.New("the model did not call the tool")
)

// Daily are the daily spending caps, as the investigator holds them.
type Daily struct {
	// Location is the time zone whose midnight starts a new day. Nil means
	// UTC.
	Location *time.Location
	// MaxCostMicros is the global cap, and zero means none.
	MaxCostMicros int64
	// RouteMicros are the caps of single routes, by route name. A route
	// that is missing, or whose cap is zero, has none of its own.
	RouteMicros map[string]int64
}

// Options configure a Caller.
type Options struct {
	// Name is the connector's name in the config, which spend_daily is
	// keyed by.
	Name string
	// Connector is the connector of model.roles.summarize, wrapped with
	// instrument.Wrap. Nil turns the Caller off.
	Connector model.Connector
	// Model is the model's ID, for its price. When it is empty the ID the
	// provider reports is used.
	Model  string
	Prices pricing.Table
	Daily  Daily
	// Retries is how many times a call that failed with a retryable error
	// is repeated.
	Retries int
	Logger  *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Caller makes summarize calls. It is safe for concurrent use.
type Caller struct {
	opts Options
}

// Request is one call.
type Request struct {
	// Route is the route whose daily spend the call counts toward.
	Route    string
	System   string
	Messages []model.Message
	// Tool is the one tool the model must call. Its schema shapes the answer.
	Tool      model.ToolSpec
	MaxTokens int
}

// New returns a Caller.
func New(opts Options) *Caller {
	if opts.Now == nil {
		opts.Now = time.Now
	}

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}

	return &Caller{opts: opts}
}

// Enabled reports whether a summarize connector is configured.
func (c *Caller) Enabled() bool {
	return c.opts.Connector != nil
}

// CallTool asks the model to call req.Tool and returns the tool's input. It
// refuses with ErrOverCap when a daily cap is reached. It prices the call
// and adds it to the spend of req.Route through q, which should be the
// transaction of the change the call is part of, so the spend commits or
// rolls back with it.
func (c *Caller) CallTool(ctx context.Context, q db.DBTX, req Request) (json.RawMessage, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}

	day := c.day()

	if err := c.checkCap(ctx, q, req.Route, day); err != nil {
		return nil, fmt.Errorf("check the daily cap: %w", err)
	}

	resp, err := model.Generate(ctx, c.opts.Connector, c.request(req), c.opts.Retries)
	if err != nil {
		return nil, fmt.Errorf("call the summarize model: %w", err)
	}

	if err := c.addSpend(ctx, q, req.Route, day, resp); err != nil {
		return nil, fmt.Errorf("count the call: %w", err)
	}

	for _, p := range resp.Message.Parts {
		if call, ok := p.(model.ToolCall); ok && call.Name == req.Tool.Name {
			return call.Input, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrNoToolCall, req.Tool.Name)
}

// request builds the model request: the one tool, forced when the
// connector can force a tool.
func (c *Caller) request(req Request) model.Request {
	choice := model.ToolChoice{Mode: model.ToolChoiceAuto}
	if c.opts.Connector.Capabilities().ToolChoice {
		choice = model.ToolChoice{Mode: model.ToolChoiceRequired, Name: req.Tool.Name}
	}

	return model.Request{
		System:     []model.Text{{Text: req.System}},
		Messages:   req.Messages,
		Tools:      []model.ToolSpec{req.Tool},
		ToolChoice: choice,
		MaxTokens:  req.MaxTokens,
		Metadata:   map[string]string{model.MetadataRole: string(model.RoleSummarize), model.MetadataRoute: req.Route},
	}
}

// checkCap returns ErrOverCap when the global cap or the route's cap is
// reached today.
func (c *Caller) checkCap(ctx context.Context, q db.DBTX, route string, day pgtype.Date) error {
	daily := c.opts.Daily
	routeCap := daily.RouteMicros[route]

	if daily.MaxCostMicros <= 0 && routeCap <= 0 {
		return nil
	}

	spent, err := db.New(q).GetDailySpend(ctx, db.GetDailySpendParams{Route: route, Day: day})
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}

	switch {
	case daily.MaxCostMicros > 0 && spent.TotalMicros >= daily.MaxCostMicros:
		return fmt.Errorf("%w: the global cap", ErrOverCap)
	case routeCap > 0 && spent.RouteMicros >= routeCap:
		return fmt.Errorf("%w: the cap of route %s", ErrOverCap, route)
	}

	return nil
}

// addSpend adds the cost and tokens of a call to the day's spend.
func (c *Caller) addSpend(ctx context.Context, q db.DBTX, route string, day pgtype.Date, resp model.Response) error {
	modelID := c.opts.Model
	if modelID == "" {
		modelID = resp.Model
	}

	var cost int64
	if price, ok := c.opts.Prices.Lookup(modelID); ok {
		cost = price.Cost(resp.Usage)
	} else {
		c.opts.Logger.WarnContext(ctx, "the summarize model has no price, so its calls cost nothing in the daily caps", "model", modelID)
	}

	err := db.New(q).AddSpend(ctx, db.AddSpendParams{
		Day: day, Route: route, Connector: c.opts.Name, Model: modelID,
		CostMicros: cost, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
	})
	if err != nil {
		return fmt.Errorf("add the call to the daily spend: %w", err)
	}

	return nil
}

// day returns today in the caps' time zone, as spend_daily stores it.
func (c *Caller) day() pgtype.Date {
	loc := c.opts.Daily.Location
	if loc == nil {
		loc = time.UTC
	}

	y, m, d := c.opts.Now().In(loc).Date()

	return pgtype.Date{Time: time.Date(y, m, d, 0, 0, 0, 0, time.UTC), Valid: true}
}

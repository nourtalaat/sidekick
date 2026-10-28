package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// StartWatcherTool is the name of the built-in tool that starts a watcher
// during an investigation.
const StartWatcherTool = "start_watcher"

// startWatcherDescription is what the model reads about start_watcher.
const startWatcherDescription = "Start a watcher: a read-only check that runs on its own schedule, apart from this " +
	"investigation, and tells responders when the service is back up. It recovers after passesToRecover passing " +
	"checks in a row and stops at its timeout. Use it when the alert is about a service being down or degraded and " +
	"a check can show it is back. Only the check types, tools and hosts listed in this tool's input schema are allowed."

var errMissingStarterOption = errors.New("new watch model starter: Pool and Checker are required")

// ModelStarterOptions set up a ModelStarter. Pool and Checker are
// required.
type ModelStarterOptions struct {
	// Pool is the database the watchers, groups and jobs are in.
	Pool *pgxpool.Pool
	// Checker checks a requested check against the deployment.
	Checker *Checker
	// Policy is what the config allows.
	Policy Policy
	// Recorder records each started watcher. Nil writes and shows the
	// start, but logs and counts nothing.
	Recorder *Recorder
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// ModelStarter starts the watchers investigations ask for with
// start_watcher, inside each route's limits. Make it with NewModelStarter;
// it is safe for concurrent use.
type ModelStarter struct {
	opts ModelStarterOptions
	now  func() time.Time
}

// ModelRequest is one start_watcher call.
type ModelRequest struct {
	// Route is the investigation's route.
	Route string
	// Strategy is the investigation's strategy, and StrategyDefaults its
	// watcherDefaults.
	Strategy         string
	StrategyDefaults Settings
	GroupID          uuid.UUID
	InvestigationID  uuid.UUID
	// StartedBySeq is the seq of the model turn that made the call.
	StartedBySeq int32
	// Input is the call's input.
	Input json.RawMessage
	// SourceTool returns the source and the source's own name of a tool
	// the investigation exposes under a qualified name.
	SourceTool func(qualified string) (sourceName, tool string, ok bool)
}

// ModelResult is how a start_watcher call ended: the started watcher, or
// why nothing started.
type ModelResult struct {
	ID   uuid.UUID
	Name string
	// Refusal says why nothing started, naming the limit; it is empty when
	// the watcher started.
	Refusal string
}

// modelInput is the input of start_watcher.
type modelInput struct {
	Name            string         `json:"name"`
	Type            Type           `json:"type"`
	URL             string         `json:"url"`
	Expect          *Expect        `json:"expect"`
	Tool            string         `json:"tool"`
	Args            map[string]any `json:"args"`
	Condition       *Condition     `json:"condition"`
	NoData          NoData         `json:"noData"`
	Require         Require        `json:"require"`
	Interval        string         `json:"interval"`
	Timeout         string         `json:"timeout"`
	PassesToRecover int            `json:"passesToRecover"`
}

// NewModelStarter returns a ModelStarter.
func NewModelStarter(opts ModelStarterOptions) (*ModelStarter, error) {
	if opts.Pool == nil || opts.Checker == nil {
		return nil, errMissingStarterOption
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &ModelStarter{opts: opts, now: now}, nil
}

// Spec returns the start_watcher tool for an investigation of route that
// exposes the source tools named in exposed. Its schema lists only the
// check types, tools and hosts the route allows. It reports false when the
// route does not offer start_watcher, or allows nothing it could start; a
// nil ModelStarter offers it nowhere.
func (m *ModelStarter) Spec(route string, exposed []string) (model.ToolSpec, bool) {
	if m == nil {
		return model.ToolSpec{}, false
	}

	r, ok := m.opts.Policy.Route(route)
	if !ok || !r.AllowModel {
		return model.ToolSpec{}, false
	}

	tools := allowedTools(r, exposed)

	types := slices.DeleteFunc(slices.Clone(r.Types), func(t Type) bool {
		return (t == TypeTool && len(tools) == 0) || (t == TypeHTTP && len(r.Hosts) == 0)
	})
	if len(types) == 0 {
		return model.ToolSpec{}, false
	}

	schema, err := json.Marshal(inputSchema(types, tools, r.Hosts))
	if err != nil {
		return model.ToolSpec{}, false
	}

	return model.ToolSpec{Name: StartWatcherTool, Description: startWatcherDescription, InputSchema: schema}, true
}

// Start checks a start_watcher call against the route's limits, the
// bounds and the counts, and starts the watcher. A call outside them gets
// a ModelResult with a Refusal that names the limit, and nothing starts;
// the error is only for a failure to store. A call repeated after a crash,
// from the same model turn, returns the watcher it started the first
// time.
func (m *ModelStarter) Start(ctx context.Context, req ModelRequest) (ModelResult, error) {
	if m == nil {
		return ModelResult{Refusal: "start_watcher is not available in this deployment"}, nil
	}

	w, refusal := m.prepare(req)
	if refusal != "" {
		return ModelResult{Name: w.Name, Refusal: refusal}, nil
	}

	var res ModelResult

	err := pgx.BeginFunc(ctx, m.opts.Pool, func(tx pgx.Tx) error {
		var err error

		res, err = m.start(ctx, tx, req, w)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})
	if err != nil {
		return ModelResult{}, fmt.Errorf("start watcher %s: %w", w.Name, err)
	}

	return res, nil
}

// start starts w in tx, holding its group's lock.
func (m *ModelStarter) start(ctx context.Context, tx pgx.Tx, req ModelRequest, w NewWatcher) (ModelResult, error) {
	queries := db.New(tx)

	if _, err := queries.LockEventGroup(ctx, w.GroupID); err != nil {
		return ModelResult{}, fmt.Errorf("lock group %s: %w", w.GroupID, err)
	}

	started, err := Start(ctx, tx, m.opts.Policy, w, m.now(), m.opts.Recorder)

	if limit, ok := errors.AsType[*LimitError](err); ok {
		return ModelResult{Name: w.Name, Refusal: limit.Error()}, nil
	}

	if errors.Is(err, ErrNameTaken) {
		return m.existing(ctx, queries, req, w)
	}

	if err != nil {
		return ModelResult{}, fmt.Errorf("start: %w", err)
	}

	return ModelResult{ID: started.ID, Name: started.Name}, nil
}

// existing returns the watcher with w's name that the same model turn
// started before, or refuses a name the group has already.
func (m *ModelStarter) existing(ctx context.Context, queries *db.Queries, req ModelRequest, w NewWatcher) (ModelResult, error) {
	found, err := queries.GetWatcherByName(ctx, db.GetWatcherByNameParams{GroupID: w.GroupID, Name: w.Name})
	if err != nil {
		return ModelResult{}, fmt.Errorf("load watcher %s: %w", w.Name, err)
	}

	sameCall := found.InvestigationID != nil && *found.InvestigationID == req.InvestigationID &&
		found.StartedBySeq != nil && *found.StartedBySeq == req.StartedBySeq
	if sameCall {
		return ModelResult{ID: found.ID, Name: found.Name}, nil
	}

	return ModelResult{Name: w.Name, Refusal: fmt.Sprintf("name: the group has a watcher named %q already; choose another name", w.Name)}, nil
}

// prepare checks a start_watcher call without the database: the input,
// the route's limits, the check's own settings, the deployment's hosts
// and the bounds. It returns the watcher to start, or a refusal.
func (m *ModelStarter) prepare(req ModelRequest) (NewWatcher, string) {
	r, ok := m.opts.Policy.Route(req.Route)
	if !ok || !r.AllowModel {
		return NewWatcher{}, fmt.Sprintf("start_watcher is not allowed on route %q", req.Route)
	}

	var in modelInput

	dec := json.NewDecoder(bytes.NewReader(req.Input))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&in); err != nil {
		return NewWatcher{}, "the input does not decode: " + err.Error()
	}

	w := NewWatcher{
		GroupID:         req.GroupID,
		InvestigationID: req.InvestigationID,
		Name:            in.Name,
		Origin:          OriginModel,
		StartedBySeq:    &req.StartedBySeq,
	}

	if !NamePattern.MatchString(in.Name) {
		return w, fmt.Sprintf("name: %q %s", in.Name, NameRule)
	}

	check, refusal := in.check(r, req.SourceTool)
	if refusal != "" {
		return w, refusal
	}

	if err := errors.Join(check.Validate(), m.opts.Checker.Validate(check)); err != nil {
		return w, strings.ReplaceAll(err.Error(), "\n", "; ")
	}

	own, refusal := in.settings()
	if refusal != "" {
		return w, refusal
	}

	settings, problems := m.opts.Policy.Resolve(r, own, Level{Path: "strategy " + req.Strategy + " watcherDefaults", Settings: req.StrategyDefaults})
	if len(problems) > 0 {
		lines := make([]string, len(problems))
		for i, p := range problems {
			lines[i] = p.String()
		}

		return w, strings.Join(lines, "; ")
	}

	w.Check = check
	w.Settings = settings

	return w, ""
}

// check returns the check the input asks for, or why the route does not
// allow it, naming the limit.
func (in modelInput) check(r RoutePolicy, sourceTool func(string) (string, string, bool)) (Check, string) {
	if !slices.Contains(r.Types, in.Type) {
		return nil, fmt.Sprintf("type: %q checks are not allowed; %s.watcherLimits.types allows %v", in.Type, r.Path, r.Types)
	}

	switch in.Type {
	case TypeHTTP:
		h := HTTPCheck{URL: in.URL}
		if in.Expect != nil {
			h.Expect = *in.Expect
		}

		if problem := r.hostProblem(h); problem != "" {
			return nil, problem
		}

		return h, ""
	case TypeTool:
		if !slices.Contains(r.Tools, in.Tool) {
			return nil, fmt.Sprintf("tool: %q is not allowed; %s.watcherLimits.tools allows %v", in.Tool, r.Path, r.Tools)
		}

		src, tool, ok := sourceTool(in.Tool)
		if !ok {
			return nil, fmt.Sprintf("tool: the investigation has no tool %q", in.Tool)
		}

		t := ToolCheck{Source: src, Tool: tool, Args: in.Args, NoData: in.NoData}
		if in.Condition != nil {
			t.Condition = *in.Condition
		}

		return t, ""
	case TypeAlert:
		return AlertCheck{Require: in.Require}, ""
	default:
		return nil, fmt.Sprintf("type: unknown check type %q", in.Type)
	}
}

// settings returns the watcher's own settings from the input.
func (in modelInput) settings() (Level, string) {
	var s Settings

	for _, f := range []struct {
		name string
		text string
		to   *time.Duration
	}{
		{name: "interval", text: in.Interval, to: &s.Interval},
		{name: "timeout", text: in.Timeout, to: &s.Timeout},
	} {
		if f.text == "" {
			continue
		}

		d, err := time.ParseDuration(f.text)
		if err != nil || d <= 0 {
			return Level{}, fmt.Sprintf("%s: %q is not a positive duration such as 30s or 10m", f.name, f.text)
		}

		*f.to = d
	}

	if in.PassesToRecover < 0 {
		return Level{}, "passesToRecover: must be at least 1"
	}

	s.PassesToRecover = in.PassesToRecover

	return Level{Settings: s}, ""
}

// allowedTools returns the route's tools that the investigation exposes,
// in the route's order.
func allowedTools(r RoutePolicy, exposed []string) []string {
	var out []string

	for _, t := range r.Tools {
		if slices.Contains(exposed, t) {
			out = append(out, t)
		}
	}

	return out
}

// inputSchema returns the input schema of start_watcher for the allowed
// types, tools and hosts.
func inputSchema(types []Type, tools, hosts []string) map[string]any {
	props := map[string]any{
		"name": map[string]any{
			"type": "string", "pattern": NamePattern.String(),
			"description": "A short name, unique in this group, such as checkout-health.",
		},
		"type":            map[string]any{"type": "string", "enum": types},
		"interval":        map[string]any{"type": "string", "description": "Time between checks, such as 30s. Optional."},
		"timeout":         map[string]any{"type": "string", "description": "How long the watcher may run without recovering, such as 60m. Optional."},
		"passesToRecover": map[string]any{"type": "integer", "minimum": 1, "description": "Passing checks in a row that count as recovered. Optional."},
	}

	if slices.Contains(types, TypeHTTP) {
		props["url"] = map[string]any{
			"type":        "string",
			"description": "For http: the URL to GET. Its host must match one of: " + strings.Join(hosts, ", ") + ".",
		}
		props["expect"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"status":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Passing statuses; default [200]."},
				"bodyRegex": map[string]any{"type": "string", "description": "A regular expression the body must match."},
			},
		}
	}

	if slices.Contains(types, TypeTool) {
		props["tool"] = map[string]any{"type": "string", "enum": tools, "description": "For tool: the tool to call."}
		props["args"] = map[string]any{"type": "object", "description": "For tool: the fixed input of the tool."}
		props["condition"] = map[string]any{
			"type": "object", "additionalProperties": false,
			"description": "For tool: jsonPath with op and value on the structured result, or textRegex on its text.",
			"properties": map[string]any{
				"jsonPath":  map[string]any{"type": "string"},
				"op":        map[string]any{"type": "string", "enum": Ops},
				"value":     map[string]any{"type": []string{"string", "number", "boolean"}},
				"textRegex": map[string]any{"type": "string"},
				"negate":    map[string]any{"type": "boolean"},
			},
		}
		props["noData"] = map[string]any{"type": "string", "enum": []NoData{NoDataFail, NoDataPass}}
	}

	if slices.Contains(types, TypeAlert) {
		props["require"] = map[string]any{
			"type": "string", "enum": []Require{RequireAll, RequireAny},
			"description": "For alert: whether all of the group's alerts must recover, or any.",
		}
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "type"},
		"properties":           props,
	}
}

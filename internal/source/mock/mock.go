package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "mock"

// noMatchMessage is the tool error of a call no fixture matches when the
// default is DefaultError.
const noMatchMessage = "no fixture matches this call"

var (
	// errUnknownTool is returned for a call to a tool the snapshot does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
	// errUnavailable is the reason an unavailable source gives.
	errUnavailable = fmt.Errorf("the scenario marks this source unavailable: %w", source.ErrUnavailable)
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source answers calls from fixtures. It is safe for concurrent use.
type Source struct {
	tools       []source.Tool
	fixtures    []fixture
	unavailable bool
	dflt        Default
	now         func() time.Time

	// mu guards the calls count of each fixture.
	mu sync.Mutex
}

var _ source.Source = (*Source)(nil)

// fixture is a Fixture with its regular expressions compiled and the
// number of calls it answered.
type fixture struct {
	Fixture

	regexes map[string]*regexp.Regexp
	calls   int
}

// snapshot is the content of a tool snapshot file.
type snapshot struct {
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"tools"`
}

// Tools returns the snapshot's tools, every one read-only. An unavailable
// source returns an error that wraps source.ErrUnavailable instead.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	if s.unavailable {
		return nil, errUnavailable
	}

	return slices.Clone(s.tools), nil
}

// Call answers from the first fixture that matches the call, or with the
// default when none does. An unavailable source returns an error that wraps
// source.ErrUnavailable.
func (s *Source) Call(_ context.Context, call source.Call) (source.Result, error) {
	if s.unavailable {
		return source.Result{}, errUnavailable
	}

	if !slices.ContainsFunc(s.tools, func(t source.Tool) bool { return t.Name == call.Tool }) {
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}

	args, ok := decodeArgs(call.Input)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.fixtures {
		f := &s.fixtures[i]
		if ok && f.matches(call.Tool, args, s.now()) {
			return f.next().result(), nil
		}
	}

	if s.dflt == DefaultError {
		return source.Result{}, &source.ToolError{Message: noMatchMessage}
	}

	return source.Result{Text: NoDataText, NoData: true}, nil
}

// Close releases nothing: the source holds no connections.
func (s *Source) Close() error {
	return nil
}

// New builds a mock source from cfg, reading its tool snapshot. Every
// fixture's tool must match at least one tool of the snapshot.
func New(_ context.Context, _ string, cfg Config, _ registry.Deps) (source.Source, error) {
	s := &Source{unavailable: cfg.Unavailable, dflt: cfg.Default, now: time.Now}

	if s.dflt == "" {
		s.dflt = DefaultNoData
	}

	if !cfg.Now.IsZero() {
		s.now = func() time.Time { return cfg.Now }
	}

	if cfg.Tools.Snapshot != "" {
		tools, err := readSnapshot(cfg.Tools.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("tools.snapshot: %w", err)
		}

		s.tools = tools
	}

	for i, f := range cfg.Fixtures {
		compiled, err := s.compile(f)
		if err != nil {
			return nil, fmt.Errorf("fixtures[%d].%w", i, err)
		}

		s.fixtures = append(s.fixtures, compiled)
	}

	return s, nil
}

// compile compiles the regular expressions of f and checks that its tool
// glob matches a tool of the snapshot.
func (s *Source) compile(f Fixture) (fixture, error) {
	if len(s.tools) > 0 && !slices.ContainsFunc(s.tools, func(t source.Tool) bool { return globMatch(f.Tool, t.Name) }) {
		return fixture{}, fmt.Errorf("tool: %q matches no tool of the snapshot", f.Tool)
	}

	out := fixture{Fixture: f, regexes: map[string]*regexp.Regexp{}}

	for name, m := range f.Match.Args {
		if m.Regex == "" {
			continue
		}

		re, err := regexp.Compile(m.Regex)
		if err != nil {
			return fixture{}, fmt.Errorf("match.args.%s.regex: %w", name, err)
		}

		out.regexes[name] = re
	}

	return out, nil
}

// matches reports whether the fixture answers a call of tool with args.
func (f *fixture) matches(tool string, args map[string]any, now time.Time) bool {
	if !globMatch(f.Tool, tool) {
		return false
	}

	if f.Strict {
		for name := range args {
			if _, ok := f.Match.Args[name]; !ok {
				return false
			}
		}
	}

	for name, m := range f.Match.Args {
		value, present := args[name]
		if !m.holds(value, present, f.regexes[name], now) {
			return false
		}
	}

	return true
}

// next returns the response to the fixture's next call and counts the
// call. The caller holds the source's mu.
func (f *fixture) next() Response {
	defer func() { f.calls++ }()

	if f.Respond != nil {
		return *f.Respond
	}

	return f.Responses[min(f.calls, len(f.Responses)-1)]
}

// result turns the response into a source result.
func (r Response) result() source.Result {
	res := source.Result{Text: r.Text, NoData: r.NoData}
	if r.NoData && res.Text == "" {
		res.Text = NoDataText
	}

	for _, e := range r.Evidence {
		res.Evidence = append(res.Evidence, source.Evidence{Kind: e.Kind, Title: e.Title, URL: e.URL})
	}

	return res
}

// readSnapshot reads the tools of a snapshot file. Each tool needs a name,
// used once, and an input schema.
func readSnapshot(file string) ([]source.Tool, error) {
	raw, err := os.ReadFile(file) //nolint:gosec // The scenario author chooses the snapshot file.
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var snap snapshot

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("decode %s: %w", file, err)
	}

	tools := make([]source.Tool, 0, len(snap.Tools))
	seen := map[string]bool{}

	for i, t := range snap.Tools {
		switch {
		case t.Name == "":
			return nil, fmt.Errorf("%s: tools[%d].name: required", file, i)
		case seen[t.Name]:
			return nil, fmt.Errorf("%s: tools[%d].name: %q is listed twice", file, i, t.Name)
		case len(t.InputSchema) == 0:
			return nil, fmt.Errorf("%s: tools[%d].inputSchema: required", file, i)
		}

		seen[t.Name] = true
		tools = append(tools, source.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema, ReadOnly: true})
	}

	return tools, nil
}

// decodeArgs decodes a call's input as a JSON object. An empty input is an
// object without arguments. It reports false for any other input, which no
// fixture matches.
func decodeArgs(input json.RawMessage) (map[string]any, bool) {
	if len(bytes.TrimSpace(input)) == 0 {
		return map[string]any{}, true
	}

	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, false
	}

	if args == nil {
		args = map[string]any{}
	}

	return args, true
}

// globMatch reports whether name matches the glob pattern, which Validate
// has checked.
func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)

	return err == nil && ok
}

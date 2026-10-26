package tools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/source"
)

// listTimeout bounds one source's Tools call when a run starts.
const listTimeout = 5 * time.Second

var (
	// errNoRedactor is returned by New without a redactor.
	errNoRedactor = errors.New("no redactor")
	// errDuplicateSource is returned by New when two sources share a name.
	errDuplicateSource = errors.New("source listed twice")
	// errNoSource is returned by New for a source without an implementation.
	errNoSource = errors.New("source has no implementation")
)

// Source is one source the guard runs calls for.
type Source struct {
	// Name is the source's name in config, the prefix of its tool names.
	Name string
	// Source is the built source.
	Source source.Source
	// Settings are the source's shared settings, from its config section
	// through source.Configured.
	Settings source.Settings
}

// Options configure a Guard.
type Options struct {
	// Redactor redacts every result and error message before the model
	// sees it. Required.
	Redactor *redact.Redactor
	// Logger gets the tools each source leaves out, and panics in sources.
	// Nil means slog.Default().
	Logger *slog.Logger
	// Metrics records the tool call metrics and sidekick_source_up. Nil
	// records none.
	Metrics *Metrics
	// Tracer starts the tool.call spans. Nil uses the global tracer
	// provider.
	Tracer trace.Tracer
}

// Guard runs source tool calls for investigations with failure isolation
// (SRC-10). Build one at start-up for all sources and share it: it is safe
// for concurrent use. The per-source concurrency limits and circuit
// breakers are per replica.
type Guard struct {
	sources  map[string]*guarded
	redactor *redact.Redactor
	logger   *slog.Logger
	metrics  *Metrics
	tracer   trace.Tracer

	// now is the clock of the breakers of Do keys.
	now func() time.Time
	// maxKeys bounds keys; it is maxKeys outside tests.
	maxKeys int

	mu sync.Mutex
	// logged holds, per source, the dropped tools last logged, so a drop
	// is logged when it changes rather than at every run.
	logged map[string]string
	// keys holds the limits of the keys of Do calls.
	keys map[string]*keyed
}

// guarded is one source with its limits.
type guarded struct {
	name     string
	src      source.Source
	settings source.Settings
	slots    chan struct{}
	breaker  *breaker
}

// Start begins one investigation's use of the named sources, or of every
// source when names is nil. It lists each source's tools once, so the
// run's tools never change while it runs. A source that cannot list its
// tools, or is not configured, has none in the run and is recorded in
// Checked as unavailable or failed.
func (g *Guard) Start(ctx context.Context, investigationID string, names []string) *Run {
	if names == nil {
		names = slices.Sorted(maps.Keys(g.sources))
	}

	r := &Run{guard: g, investigationID: investigationID, tools: map[string]runTool{}, checks: map[string]*check{}}

	lists := make([]sourceTools, len(names))

	var wg sync.WaitGroup

	for i, name := range names {
		wg.Go(func() { lists[i] = g.list(ctx, name) })
	}

	wg.Wait()

	for _, l := range lists {
		if l.err != nil {
			r.record(l.name, l.err)

			continue
		}

		exposed, dropped := Expose(l.src.name, l.src.settings, l.tools)
		g.logDrops(l.src.name, dropped)

		for _, e := range exposed {
			r.tools[e.Name] = runTool{src: l.src, exposed: e}
			r.specs = append(r.specs, model.ToolSpec{Name: e.Name, Description: e.Tool.Description, InputSchema: e.Tool.InputSchema})
		}
	}

	return r
}

// sourceTools is one source's tool list at the start of a run, or the
// error that stopped the source from listing.
type sourceTools struct {
	name  string
	src   *guarded
	tools []source.Tool
	err   *ToolError
}

// list lists the tools of the named source, within listTimeout.
func (g *Guard) list(ctx context.Context, name string) sourceTools {
	src, ok := g.sources[name]
	if !ok {
		return sourceTools{name: name, err: &ToolError{Kind: KindUnavailable, Source: name, Message: "the source is not configured"}}
	}

	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	tools, err := src.listTools(ctx)
	if err != nil {
		return sourceTools{name: name, err: g.classify(name, "", err).err}
	}

	return sourceTools{name: name, src: src, tools: tools}
}

// logDrops logs the tools a source leaves out, once for each change.
func (g *Guard) logDrops(name string, dropped []Dropped) {
	var b strings.Builder
	for _, d := range dropped {
		b.WriteString(d.Tool + "\x00" + string(d.Reason) + "\n")
	}

	g.mu.Lock()
	changed := g.logged[name] != b.String()
	g.logged[name] = b.String()
	g.mu.Unlock()

	if !changed {
		return
	}

	for _, d := range dropped {
		g.logger.Info("a source tool is not shown to the model", "source", name, "tool", d.Tool, "reason", d.Reason, "detail", d.Detail)
	}
}

// outcome is the classified end of one source call.
type outcome struct {
	result source.Result
	err    *ToolError
	health callResult
}

// classify turns the error of a source call into a ToolError and what it
// says about the source's health.
func (g *Guard) classify(name, tool string, err error) outcome {
	terr := &ToolError{Source: name, Tool: tool}

	var toolErr *source.ToolError

	switch {
	case errors.As(err, &toolErr):
		terr.Kind = KindToolError
		terr.Message = g.redactMessage(toolErr.Message)

		return outcome{err: terr, health: callOK}
	case errors.Is(err, source.ErrUnavailable):
		terr.Kind = KindUnavailable
		terr.Message = "the source is not connected"
	case errors.Is(err, errPanic):
		terr.Kind = KindFailed
		terr.Message = "the source failed unexpectedly"
	case errors.Is(err, context.DeadlineExceeded):
		terr.Kind = KindTimeout
		terr.Message = "the source did not answer in time"
	default:
		terr.Kind = KindFailed
		terr.Message = g.redactMessage(err.Error())
	}

	return outcome{err: terr, health: callFailed}
}

// redactMessage redacts an error message and cuts it to maxMessageLen.
func (g *Guard) redactMessage(msg string) string {
	return source.CutText(g.redactor.Text(msg).Text, maxMessageLen)
}

// listTools lists the source's tools, recovering from a panic.
func (s *guarded) listTools(ctx context.Context) (tools []source.Tool, err error) {
	defer recoverInto(&err)

	tools, err = s.src.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	return tools, nil
}

// New builds a guard for the sources. It does not call them.
func New(sources []Source, opts Options) (*Guard, error) {
	if opts.Redactor == nil {
		return nil, errNoRedactor
	}

	g := &Guard{
		sources:  map[string]*guarded{},
		redactor: opts.Redactor,
		logger:   cmp.Or(opts.Logger, slog.Default()).With("component", "tools"),
		metrics:  opts.Metrics,
		tracer:   opts.Tracer,
		now:      time.Now,
		maxKeys:  maxKeys,
		logged:   map[string]string{},
		keys:     map[string]*keyed{},
	}

	if g.tracer == nil {
		g.tracer = otel.Tracer(TracerName)
	}

	for _, s := range sources {
		if _, ok := g.sources[s.Name]; ok {
			return nil, fmt.Errorf("source %q: %w", s.Name, errDuplicateSource)
		}

		if s.Source == nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, errNoSource)
		}

		g.sources[s.Name] = &guarded{
			name:     s.Name,
			src:      s.Source,
			settings: s.Settings,
			slots:    make(chan struct{}, s.Settings.MaxConcurrency()),
			breaker:  &breaker{now: time.Now},
		}
	}

	g.metrics.watch(g)

	return g, nil
}

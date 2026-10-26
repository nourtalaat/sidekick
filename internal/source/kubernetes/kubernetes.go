package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "kubernetes"

// errUnknownTool is returned for a call to a tool the source does not
// offer. The guard sends only calls for listed tools, so it means a bug.
var errUnknownTool = errors.New("unknown tool")

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads one Kubernetes cluster. It is safe for concurrent use.
type Source struct {
	name     string
	cfg      Config
	clients  clientFunc
	links    linker
	now      func() time.Time
	tools    []source.Tool
	handlers map[string]handler
}

var _ source.Source = (*Source)(nil)

// handler runs one tool with its raw input.
type handler func(ctx context.Context, c kubernetes.Interface, input json.RawMessage) (source.Result, error)

// Tools returns the source's tools. get_pod_logs is listed only when logs
// are on. Every tool only reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return append([]source.Tool(nil), s.tools...), nil
}

// Call runs one tool within the source's timeout and the call's deadline.
// A request the API server refuses, such as an object that does not exist
// or a permission the service account lacks, returns a *source.ToolError
// that says why.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	run, ok := s.handlers[call.Tool]
	if !ok {
		return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, errUnknownTool)
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout())
	defer cancel()

	if !call.Deadline.IsZero() {
		var cancelDeadline context.CancelFunc

		ctx, cancelDeadline = context.WithDeadline(ctx, call.Deadline)
		defer cancelDeadline()
	}

	client, err := s.clients(ctx)
	if err != nil {
		return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, err)
	}

	res, err := run(ctx, client, call.Input)
	if err != nil {
		return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, err)
	}

	return res, nil
}

// Close releases nothing: client-go keeps only idle connections.
func (s *Source) Close() error {
	return nil
}

// New builds a Kubernetes source from its validated config. It makes no
// network call. With kubeconfigSecret, the Secret is read on each call, so
// a rotated file is used without a restart; without it, the pod's service
// account is used, and New fails outside a cluster.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	clients, err := newClients(cfg, deps)
	if err != nil {
		return nil, fmt.Errorf("make the Kubernetes client: %w", err)
	}

	return newSource(name, cfg, clients, time.Now)
}

// newSource builds the source around a way to get a client and a clock.
func newSource(name string, cfg Config, clients clientFunc, now func() time.Time) (*Source, error) {
	s := &Source{name: name, cfg: cfg, clients: clients, now: now}

	if cfg.ConsoleURL != "" {
		tmpl, err := parseConsoleURL(cfg.ConsoleURL)
		if err != nil {
			return nil, fmt.Errorf("consoleURL: %w", err)
		}

		s.links.console = tmpl
	}

	tools, handlers, err := s.toolset()
	if err != nil {
		return nil, fmt.Errorf("describe the tools: %w", err)
	}

	s.tools, s.handlers = tools, handlers

	return s, nil
}

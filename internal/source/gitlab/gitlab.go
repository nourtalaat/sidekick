package gitlab

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// typeName is the source type this package registers.
const typeName = "gitlab"

// maxPageSize is the most items GitLab returns in one page of a list.
const maxPageSize = 100

var (
	// errNoSecrets is returned when the source is built without a way to
	// read its token.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(typeName, registry.Typed(New))
}

// Source reads GitLab projects with one access token. It is safe for
// concurrent use.
type Source struct {
	cfg    Config
	client *client
	// pageSize is how many items one page of a list asks for. Tests make it
	// small to page through short lists.
	pageSize int
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A project, ref or path that does not exist, and a
// list with nothing in it, return a result with NoData set. A call GitLab
// refuses, such as a ref it cannot parse, returns a *source.ToolError.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	res, err := s.run(ctx, call)
	if err != nil {
		return failure(call.Tool, err)
	}

	return res, nil
}

// Close releases nothing: the source holds no connections of its own.
func (s *Source) Close() error {
	return nil
}

// run sends a call to its tool.
func (s *Source) run(ctx context.Context, call source.Call) (source.Result, error) {
	switch call.Tool {
	case toolListCommits:
		return s.listCommits(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds a GitLab source from its validated config. It makes no network
// call: the token is read and used by the first tool call, so GitLab being
// down blocks neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	c, err := newClient(cfg, deps)
	if err != nil {
		return nil, fmt.Errorf("make the GitLab client: %w", err)
	}

	return &Source{cfg: cfg, client: c, pageSize: maxPageSize}, nil
}

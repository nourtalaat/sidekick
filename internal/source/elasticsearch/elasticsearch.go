package elasticsearch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"text/template"
	"time"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "elasticsearch"

var (
	// errNoSecrets is returned when the source is built without a way to
	// read its credentials.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads the logs of one Elasticsearch or OpenSearch cluster. It is
// safe for concurrent use.
type Source struct {
	cfg    Config
	client *client
	// discover is the parsed discoverURL template, or nil.
	discover *template.Template
	now      func() time.Time
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A search that matches nothing and an index that does
// not exist return a result with NoData set. An index the config does not
// allow, input that does not fit and a request the server refuses, such
// as a query string it cannot parse, return a *source.ToolError.
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
	case toolSearchLogs:
		return s.searchLogs(ctx, call.Input)
	case toolCountLogs:
		return s.countLogs(ctx, call.Input)
	case toolListFields:
		return s.listFields(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds a source from its validated config. It makes no network
// call, so the cluster being down blocks neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	var discover *template.Template

	if cfg.DiscoverURL != "" {
		tmpl, err := parseDiscoverURL(cfg.DiscoverURL)
		if err != nil {
			return nil, fmt.Errorf("discoverURL: %w", err)
		}

		discover = tmpl
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	a := auth{secrets: deps.Secrets, apiKey: cfg.APIKeySecret}
	if cfg.BasicAuth != nil {
		a.username = cfg.BasicAuth.Username
		a.password = cfg.BasicAuth.PasswordSecret
	}

	return &Source{
		cfg: cfg,
		client: &client{
			http:      httpClient,
			apiURL:    cfg.apiURL(),
			product:   cfg.product(),
			userAgent: "sidekick/" + buildinfo.Version,
			auth:      a,
		},
		discover: discover,
		now:      time.Now,
	}, nil
}

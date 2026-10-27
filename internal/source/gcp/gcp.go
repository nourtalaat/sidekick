package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "gcp"

// The REST API roots the source calls, each with its pinned API version.
const (
	loggingAPI       = "https://logging.googleapis.com/v2"
	monitoringAPI    = "https://monitoring.googleapis.com/v3"
	prometheusAPI    = "https://monitoring.googleapis.com/v1"
	containerAPI     = "https://container.googleapis.com/v1"
	serviceHealthAPI = "https://servicehealth.googleapis.com/v1"
)

var (
	// errNoSecrets is returned when credentialsSecret is set but the source
	// is built without a way to read it.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads Google Cloud projects with one identity. It is safe for
// concurrent use.
type Source struct {
	cfg    Config
	client *client
	apis   endpoints
	now    func() time.Time
}

// endpoints are the API roots a source calls. Tests point them at a fake
// server.
type endpoints struct {
	logging       string
	monitoring    string
	prometheus    string
	container     string
	serviceHealth string
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A resource that does not exist and a query that
// matches nothing return a result with NoData set. A project the config
// does not list, input that does not fit and a request Google Cloud
// refuses, such as a filter it cannot parse, return a *source.ToolError.
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
	case toolQueryLogs:
		return s.queryLogs(ctx, call.Input)
	case toolQueryMetrics:
		return s.queryMetrics(ctx, call.Input)
	case toolQueryPromQL:
		return s.queryPromQL(ctx, call.Input)
	case toolListAlerts:
		return s.listAlerts(ctx, call.Input)
	case toolListChanges:
		return s.listChanges(ctx, call.Input)
	case toolGetGKECluster:
		return s.getGKECluster(ctx, call.Input)
	case toolListServiceHealthEvents:
		return s.listServiceHealthEvents(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds a Google Cloud source from its validated config. It makes no
// network call: credentials are found, and the first token made, by the
// first tool call, so Google Cloud being down blocks neither start-up nor
// readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if cfg.CredentialsSecret != nil && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	base := deps.HTTP
	if base == nil {
		base = httpclient.New()
	}

	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	limited := *base
	limited.Transport = limitTransport{next: next}

	c := &client{
		http:      &limited,
		tokens:    newTokens(cfg.CredentialsSecret, deps.Secrets, &limited),
		userAgent: "sidekick/" + buildinfo.Version,
	}

	return &Source{
		cfg:    cfg,
		client: c,
		apis: endpoints{
			logging:       loggingAPI,
			monitoring:    monitoringAPI,
			prometheus:    prometheusAPI,
			container:     containerAPI,
			serviceHealth: serviceHealthAPI,
		},
		now: time.Now,
	}, nil
}

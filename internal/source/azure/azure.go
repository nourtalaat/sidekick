package azure

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
const Type = "azure"

// The API roots the source calls.
const (
	armAPI = "https://management.azure.com"
	// logAnalyticsAPI is the Log Analytics query API, with its pinned
	// version.
	logAnalyticsAPI = "https://api.loganalytics.io/v1"
)

// The scopes of the tokens for each API.
const (
	armScope          = "https://management.azure.com/.default"
	logAnalyticsScope = "https://api.loganalytics.io/.default"
)

// The pinned api-version of each Azure Resource Manager API the source
// calls.
const (
	metricsVersion     = "2024-02-01"
	activityLogVersion = "2015-04-01"
	// alertsVersion is a preview: Alerts Management has no stable
	// version of the alerts list.
	alertsVersion         = "2023-07-12-preview"
	resourceHealthVersion = "2025-05-01"
	aksVersion            = "2026-08-01"
)

var (
	// errNoSecrets is returned when clientSecret is set but the source is
	// built without a way to read it.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads Azure subscriptions and workspaces with one identity. It is
// safe for concurrent use.
type Source struct {
	cfg    Config
	client *client
	apis   endpoints
	now    func() time.Time
}

// endpoints are the API roots a source calls. Tests point them at a fake
// server.
type endpoints struct {
	arm          string
	logAnalytics string
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A resource that does not exist and a query that
// returns nothing give a result with NoData set. A subscription, resource
// or workspace the config does not allow, input that does not fit and a
// request Azure refuses, such as a query it cannot parse, return a
// *source.ToolError.
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
	case toolListAlerts:
		return s.listAlerts(ctx, call.Input)
	case toolListChanges:
		return s.listChanges(ctx, call.Input)
	case toolGetResourceHealth:
		return s.getResourceHealth(ctx, call.Input)
	case toolGetAKSCluster:
		return s.getAKSCluster(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds an Azure source from its validated config. It makes no
// network call: the credential is set up, and the first token made, by the
// first tool call, so Azure being down blocks neither start-up nor
// readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if cfg.ClientSecret != nil && deps.Secrets == nil {
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
		tokens:    newTokens(newCredential(cfg, deps.Secrets, &limited)),
		userAgent: "sidekick/" + buildinfo.Version,
	}

	return &Source{
		cfg:    cfg,
		client: c,
		apis:   endpoints{arm: armAPI, logAnalytics: logAnalyticsAPI},
		now:    time.Now,
	}, nil
}

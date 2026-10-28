package prometheus

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/prometheus/promql/parser"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "prometheus"

var (
	// errNoSecrets is returned when the config names a secret but the
	// source is built without a way to read it.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errNoTransport is returned when tls is set but the HTTP client in
	// deps has a transport whose TLS setup cannot be changed.
	errNoTransport = errors.New("tls needs an *http.Transport in the HTTP client of deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads one Prometheus-compatible server, and optionally an
// Alertmanager. It is safe for concurrent use.
type Source struct {
	prom *client
	// alertmanager is nil when the config has no alertmanagerURL.
	alertmanager *client
	links        links
	parser       parser.Parser
	now          func() time.Time
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A query that matches nothing and an API the server
// does not have return a result with NoData set. A query or selector that
// does not parse, input that does not fit and a request the server
// refuses, such as a query that fails while it runs, return a
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
	case toolQuery:
		return s.query(ctx, call.Input)
	case toolQueryRange:
		return s.queryRange(ctx, call.Input)
	case toolListAlerts:
		return s.listAlerts(ctx, call.Input)
	case toolGetRules:
		return s.getRules(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds a Prometheus source from its validated config. It makes no
// network call and reads no secret, so the server being down blocks
// neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if cfg.usesSecrets() && deps.Secrets == nil {
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

	if cfg.TLS != nil {
		t, ok := next.(*http.Transport)
		if !ok {
			return nil, errNoTransport
		}

		next = newTLSTransport(t, *cfg.TLS, deps.Secrets)
	}

	limited := *base
	limited.Transport = limitTransport{next: next}

	a := &auth{secrets: deps.Secrets, bearer: cfg.BearerTokenSecret, basic: cfg.BasicAuth, headers: cfg.Headers}
	userAgent := "sidekick/" + buildinfo.Version

	s := &Source{
		prom: &client{http: &limited, root: strings.TrimSuffix(cfg.BaseURL, "/"), service: servicePrometheus, userAgent: userAgent, auth: a},
		links: links{
			prometheus: cfg.prometheusURL(),
			grafana:    strings.TrimSuffix(cfg.Links.GrafanaURL, "/"),
			datasource: cfg.Links.GrafanaDatasource,
		},
		parser: newParser(),
		now:    time.Now,
	}

	if cfg.AlertmanagerURL != "" {
		s.alertmanager = &client{
			http: &limited, root: strings.TrimSuffix(cfg.AlertmanagerURL, "/"), service: serviceAlertmanager, userAgent: userAgent, auth: a,
		}
	}

	return s, nil
}

// newParser returns the PromQL parser of the source. It accepts every
// optional PromQL feature, since the server that runs the query decides
// which ones it allows.
func newParser() parser.Parser {
	return parser.NewParser(parser.Options{
		EnableExperimentalFunctions:  true,
		ExperimentalDurationExpr:     true,
		EnableExtendedRangeSelectors: true,
		EnableBinopFillModifiers:     true,
	})
}

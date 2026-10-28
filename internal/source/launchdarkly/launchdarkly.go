package launchdarkly

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// Type is the source type this package registers.
const Type = "launchdarkly"

// service is the name the tools and results give LaunchDarkly.
const service = "LaunchDarkly"

// notFoundText is the text of a result for a flag or project LaunchDarkly
// does not have.
const notFoundText = "Not found in LaunchDarkly: the project or flag does not exist, or Sidekick's token cannot read it."

var (
	// errNoSecrets is returned when the source is built without a way to
	// read its token.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

// toolList is what Tools returns. Every tool only reads.
var toolList = flags.Tools(service)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads one LaunchDarkly account. It is safe for concurrent use.
type Source struct {
	cfg    Config
	client *client
	now    func() time.Time
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A flag or project LaunchDarkly does not have, and a
// range without changes, return a result with NoData set. A project or
// environment the config does not list, input that does not fit and a
// request LaunchDarkly refuses return a *source.ToolError.
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
	case flags.ToolListChanges:
		return s.listFlagChanges(ctx, call.Input)
	case flags.ToolGetFlag:
		return s.getFlag(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// flagURL returns the link to a flag's page in one environment, or to the
// environment's flag list when flag is empty.
func (s *Source) flagURL(scope flags.Scope, flag string) string {
	u := s.cfg.baseURL() + "/" + url.PathEscape(scope.Project) + "/" + url.PathEscape(scope.Environment) + "/features"
	if flag != "" {
		u += "/" + url.PathEscape(flag)
	}

	return u
}

// siteURL returns the link LaunchDarkly gives for a resource, as a path
// under the base URL. Anything but such a path, such as a link to another
// host, is left out, since the text comes from LaunchDarkly.
func (s *Source) siteURL(href string) string {
	if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") || strings.ContainsAny(href, "\\\r\n") {
		return ""
	}

	return s.cfg.baseURL() + href
}

// New builds a LaunchDarkly source from its validated config. It makes no
// network call, so LaunchDarkly being down blocks neither start-up nor
// readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	return &Source{
		cfg: cfg,
		client: &client{
			http:      httpClient,
			baseURL:   cfg.baseURL(),
			userAgent: "sidekick/" + buildinfo.Version,
			token:     cfg.TokenSecret,
			secrets:   deps.Secrets,
		},
		now: time.Now,
	}, nil
}

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. Other refusals of the call itself, such as a 400
// for a request LaunchDarkly cannot read or a 403 for a resource the
// token's role may not read, are tool errors with LaunchDarkly's message.
// A refused token, rate limits, server errors and secret failures are
// failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		switch code := se.code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

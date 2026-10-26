package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "github"

// maxPageSize is the most items GitHub returns in one page of a list.
const maxPageSize = 100

var (
	// errNoSecrets is returned when the source is built without a way to
	// read the app's private key.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errUnknownTool is returned for a call to a tool the source does not
	// have. The guard only sends calls for tools the source listed, so this
	// means a bug.
	errUnknownTool = errors.New("unknown tool")
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads GitHub repositories as one GitHub App installation. It is
// safe for concurrent use.
type Source struct {
	cfg    Config
	client *gh.Client
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

// Call runs one tool. A repository, ref or path that does not exist, and a
// list with nothing in it, return a result with NoData set. A call GitHub
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
	case toolGetCommit:
		return s.getCommit(ctx, call.Input)
	case toolListPullRequests:
		return s.listPullRequests(ctx, call.Input)
	case toolListReleases:
		return s.listReleases(ctx, call.Input)
	case toolListWorkflowRuns:
		return s.listWorkflowRuns(ctx, call.Input)
	case toolGetFile:
		return s.getFile(ctx, call.Input)
	case toolCompare:
		return s.compare(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds a GitHub source from its validated config. It makes no
// network call: the first installation token is made by the first tool
// call, so GitHub being down blocks neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if deps.Secrets == nil {
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

	baseURL := cfg.baseURL()

	origin, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse the base URL: %w", err)
	}

	userAgent := "sidekick/" + buildinfo.Version
	limited := limitTransport{next: next}

	exchange := *base
	exchange.Transport = limited

	tokens := &installationTokens{
		appID:          cfg.App.AppID,
		installationID: cfg.App.InstallationID,
		privateKey:     cfg.App.PrivateKeySecret,
		secrets:        deps.Secrets,
		client:         &exchange,
		baseURL:        baseURL,
		userAgent:      userAgent,
		now:            time.Now,
	}

	api := *base
	api.Transport = &authTransport{next: limited, origin: origin, tokens: tokens}

	client, err := gh.NewClient(gh.WithHTTPClient(&api), gh.WithURLs(&baseURL, nil), gh.WithUserAgent(userAgent))
	if err != nil {
		return nil, fmt.Errorf("make the go-github client: %w", err)
	}

	return &Source{cfg: cfg, client: client, pageSize: maxPageSize}, nil
}

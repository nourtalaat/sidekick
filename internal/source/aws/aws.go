package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "aws"

// appID names Sidekick in the user agent of every AWS request, so CloudTrail
// shows which calls it made.
const appID = "sidekick"

// pollInterval is how often query_logs asks whether its query finished.
const pollInterval = time.Second

// errUnknownTool is returned for a call to a tool the source does not have.
// The guard only sends calls for tools the source listed, so this means a
// bug.
var errUnknownTool = errors.New("unknown tool")

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source reads AWS accounts and Regions. It is safe for concurrent use.
type Source struct {
	cfg Config
	// base is the AWS config every client starts from: the HTTP client,
	// the retry rules and Sidekick's own credentials.
	base     awssdk.Config
	accounts map[string]*account
	apis     endpoints
	now      func() time.Time
	// pollEvery is how often query_logs polls its query.
	pollEvery time.Duration
}

// endpoints replace the AWS address of each API the source calls. Empty
// means the address the SDK resolves for the Region. Tests point them at
// fake servers.
type endpoints struct {
	logs       string
	cloudTrail string
	sts        string
}

var _ source.Source = (*Source)(nil)

// Tools returns the source's tools. They never change, and every one only
// reads.
func (s *Source) Tools(context.Context) ([]source.Tool, error) {
	return slices.Clone(toolList), nil
}

// Call runs one tool. A log group or resource that does not exist and a
// query that returns nothing give a result with NoData set. An account,
// Region or log group the config does not allow, input that does not fit
// and a request AWS refuses, such as a query it cannot parse or a
// permission the role lacks, return a *source.ToolError.
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
	case toolListChanges:
		return s.listChanges(ctx, call.Input)
	default:
		return source.Result{}, fmt.Errorf("tool %q: %w", call.Tool, errUnknownTool)
	}
}

// New builds an AWS source from its validated config. It makes no network
// call: credentials are read, and roles assumed, by the first tool call for
// each account, so AWS being down blocks neither start-up nor readiness.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	return build(ctx, name, cfg, deps, config.LoadDefaultConfig, endpoints{})
}

// build is New with the AWS config loader and the API addresses to use.
func build(ctx context.Context, name string, cfg Config, deps registry.Deps, load awsauth.Loader, apis endpoints) (*Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("check the config: %w", err)
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

	awsCfg, err := awsauth.Load(ctx, load, cfg.Regions[0], &limited)
	if err != nil {
		return nil, fmt.Errorf("set up AWS credentials: %w", err)
	}

	// Every API address comes from the SDK or from apis, never from
	// AWS_ENDPOINT_URL, so calls go only to the Regions in the config.
	awsCfg.BaseEndpoint = nil
	awsCfg.AppID = appID

	stsClient := sts.NewFromConfig(awsCfg, func(o *sts.Options) {
		o.BaseEndpoint = endpoint(apis.sts)
	})

	return &Source{
		cfg:       cfg,
		base:      awsCfg,
		accounts:  newAccounts(cfg.Accounts, awsCfg.Credentials, stsClient),
		apis:      apis,
		now:       time.Now,
		pollEvery: pollInterval,
	}, nil
}

// endpoint returns the address an API override gives, or nil for the
// address the SDK resolves.
func endpoint(override string) *string {
	if override == "" {
		return nil
	}

	return awssdk.String(override)
}

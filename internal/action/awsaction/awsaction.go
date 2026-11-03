// Package awsaction is what the AWS action types share: the accounts and
// Regions of their config, and the credentials and clients they call AWS
// with. It holds no code of any one AWS service. A type such as
// aws.ecs_rollback embeds Config in its own config, builds Clients with New,
// picks the account and Region of a call with Clients.Target, and makes the
// client of its service from Target.Config:
//
//	client := ecs.NewFromConfig(target.Config)
//
// Every account must name a role. Sidekick assumes it with the session name
// "sidekick" and the optional external ID, so an action never uses the
// credentials Sidekick has for its sources, and the role's policy is the hard
// limit on what the action can change.
package awsaction

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/source"
)

// appID names Sidekick in the user agent of every AWS request, so CloudTrail
// shows which calls it made.
const appID = "sidekick"

// maxShownLen is the most bytes of a value from the model that an error
// message shows.
const maxShownLen = 512

// Account is one AWS account an action changes. Its roleARN is required.
type Account = awsauth.Account

// Config is embedded, inline, in the config of every AWS action:
//
//	awsaction.Config `yaml:",inline"`
//
// It adds the keys accounts and regions. A call for any other account or
// Region is refused before anything is sent.
type Config struct {
	// Accounts are the AWS accounts the action may change. Each has an id
	// (12 digits; quote it in YAML), a roleARN in that account, and
	// optionally the externalID the role's trust policy asks for.
	Accounts []Account `yaml:"accounts"`
	// Regions are the Regions the action may change, in every account.
	Regions []string `yaml:"regions"`
}

// Validate checks the accounts and Regions without credentials or network
// calls. Each error starts with the key of its field.
func (c Config) Validate() error {
	errs := awsauth.CheckAccounts(c.Accounts, awsauth.RequireRole())

	return errors.Join(append(errs, awsauth.CheckRegions(c.Regions)...)...)
}

// Clients holds the credentials of every account of a config. It is safe for
// concurrent use.
type Clients struct {
	cfg   Config
	base  aws.Config
	creds map[string]aws.CredentialsProvider
}

// Option changes how New builds the clients. Tests use them to keep AWS out.
type Option func(*options)

type options struct {
	load        awsauth.Loader
	stsEndpoint string
}

// WithLoader loads the AWS config with load, in place of the default AWS
// chain.
func WithLoader(load awsauth.Loader) Option {
	return func(o *options) { o.load = load }
}

// WithSTSEndpoint sends the AssumeRole calls to url, in place of the STS
// address of the Region.
func WithSTSEndpoint(url string) Option {
	return func(o *options) { o.stsEndpoint = url }
}

// New builds the clients for the accounts of cfg, with httpClient for every
// request, or the default client when it is nil. It checks cfg and makes no
// network call: credentials are first read, and roles assumed, by the first
// Target.Ready of an account, so AWS being down does not stop Sidekick from
// starting. Every API address comes from the SDK, never from
// AWS_ENDPOINT_URL, so calls go only to the Regions in the config.
func New(ctx context.Context, cfg Config, httpClient *http.Client, opts ...Option) (*Clients, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("check the config: %w", err)
	}

	base, err := awsauth.Load(ctx, o.load, cfg.Regions[0], awsauth.HTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("set up AWS credentials: %w", err)
	}

	base.BaseEndpoint = nil
	base.AppID = appID

	stsClient := sts.NewFromConfig(base, func(so *sts.Options) {
		if o.stsEndpoint != "" {
			so.BaseEndpoint = aws.String(o.stsEndpoint)
		}
	})

	creds := make(map[string]aws.CredentialsProvider, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		creds[a.ID] = awsauth.AssumeRole(stsClient, a.RoleARN, a.ExternalID, awsauth.SessionName)
	}

	return &Clients{cfg: cfg, base: base, creds: creds}, nil
}

// Target is the account and Region of one call.
type Target struct {
	Account string
	Region  string
	// Config is the AWS config for the call: the Region and the credentials
	// of the account's role are set. Make the client of a service from it.
	Config aws.Config
}

// Target returns the account and Region a call names. With one account or
// one Region in the config, the call may leave it out; the error then wraps
// action.ErrParams when it must be given, and action.ErrLimit when the
// config does not list the one given. Both name the allowed ones, for the
// model to read.
func (c *Clients) Target(account, region string) (Target, error) {
	ids := make([]string, 0, len(c.cfg.Accounts))
	for _, a := range c.cfg.Accounts {
		ids = append(ids, a.ID)
	}

	id, err := pick("account", account, ids)
	if err != nil {
		return Target{}, fmt.Errorf("pick the account: %w", err)
	}

	r, err := pick("Region", region, c.cfg.Regions)
	if err != nil {
		return Target{}, fmt.Errorf("pick the Region: %w", err)
	}

	cfg := c.base.Copy()
	cfg.Region = r
	cfg.Credentials = c.creds[id]

	return Target{Account: id, Region: r, Config: cfg}, nil
}

// Ready gets the credentials of the call's account, which assumes its role,
// so a failure to get them is reported before any API call. A refusal of the
// role wraps action.ErrPermanent.
func (t Target) Ready(ctx context.Context) error {
	if _, err := t.Config.Credentials.Retrieve(ctx); err != nil {
		return fmt.Errorf("get credentials for account %s: %w", t.Account, Fail(err))
	}

	return nil
}

// pick returns the one of allowed that value names. A blank value is the one
// allowed value when there is only one.
func pick(key, value string, allowed []string) (string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		if len(allowed) == 1 {
			return allowed[0], nil
		}

		return "", fmt.Errorf("give %s; allowed: %s: %w", key, strings.Join(allowed, ", "), action.ErrParams)
	}

	for _, a := range allowed {
		if value == a {
			return a, nil
		}
	}

	return "", fmt.Errorf("%s %q is not one Sidekick may change; allowed: %s: %w",
		key, source.CutText(value, maxShownLen), strings.Join(allowed, ", "), action.ErrLimit)
}

// ConsoleRoot returns the address of the AWS console of region, in its
// partition. It is awsauth.ConsoleRoot, here so an AWS action needs one
// import for what it shows a person.
func ConsoleRoot(region string) string {
	return awsauth.ConsoleRoot(region)
}

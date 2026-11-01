package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/health"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// sessionName is the session name of every role Sidekick assumes, which
// CloudTrail shows as the caller.
const sessionName = "sidekick"

// maxResponseSize bounds one response body. The largest answers are Logs
// Insights results; results are cut much smaller later.
const maxResponseSize = 16 << 20

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("the response from AWS is larger than %d bytes", maxResponseSize)
	// errOtherAccount is returned when Sidekick's own credentials belong to
	// another account than the one the config lists without a role.
	errOtherAccount = errors.New("the credentials Sidekick has belong to another account")
)

// account is one account in the config, with the credentials for it.
type account struct {
	id    string
	creds awssdk.CredentialsProvider
	// own is true when the account uses Sidekick's own credentials. Before
	// the first call, the source checks that they belong to the account.
	own bool
	sts *sts.Client

	mu      sync.Mutex
	checked bool
}

// target is the account and Region of one call.
type target struct {
	account *account
	region  string
}

// limitTransport fails the read of any response body larger than
// maxResponseSize, so a huge answer cannot make Sidekick read without end.
type limitTransport struct {
	next http.RoundTripper
}

// plainBody is a request body that shows only Read and Close. The AWS SDK
// sends a body that also has WriteTo, and WriteTo of that body returns
// io.EOF once the SDK has closed it. The HTTP transport reads the body one
// last time after it has sent it, with WriteTo, and treats that io.EOF as a
// failed write: it closes the connection, even when the response is half
// read. Read of a closed body returns io.EOF too, which the transport takes
// as the end of the body.
type plainBody struct {
	body io.ReadCloser
}

// credentialError is a failure to get credentials for an account: a role
// that cannot be assumed, or no credentials at all. It is a failure of the
// source, never a tool error.
type credentialError struct {
	account string
	err     error
}

// Error names the account and the cause.
func (e *credentialError) Error() string {
	return fmt.Sprintf("get credentials for account %s: %v", e.account, e.err)
}

// Unwrap returns the cause.
func (e *credentialError) Unwrap() error {
	return e.err
}

// RoundTrip implements http.RoundTripper.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(withPlainBody(req))
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	return resp, nil
}

// Read implements io.Reader.
func (b plainBody) Read(p []byte) (int, error) {
	return b.body.Read(p) //nolint:wrapcheck // io.Reader callers compare err with io.EOF, so it must stay unwrapped.
}

// Close implements io.Closer.
func (b plainBody) Close() error {
	if err := b.body.Close(); err != nil {
		return fmt.Errorf("close the request body: %w", err)
	}

	return nil
}

// ready gets the credentials of the call's account, so a failure to get
// them is reported as one before any API call. For an account that uses
// Sidekick's own credentials, the first call also checks with STS that
// they belong to it.
func (a *account) ready(ctx context.Context) error {
	if _, err := a.creds.Retrieve(ctx); err != nil {
		return &credentialError{account: a.id, err: err}
	}

	if !a.own {
		return nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.checked {
		return nil
	}

	out, err := a.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return &credentialError{account: a.id, err: fmt.Errorf("check which account the credentials belong to: %w", err)}
	}

	if got := awssdk.ToString(out.Account); got != a.id {
		return &credentialError{account: a.id, err: fmt.Errorf("%w: %s; give the account a roleARN", errOtherAccount, got)}
	}

	a.checked = true

	return nil
}

// logs returns a CloudWatch Logs client for the call's account and Region.
func (s *Source) logs(t target) *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(s.base, func(o *cloudwatchlogs.Options) {
		o.Region = t.region
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.logs)
	})
}

// cloudTrail returns a CloudTrail client for the call's account and
// Region.
func (s *Source) cloudTrail(t target) *cloudtrail.Client {
	return cloudtrail.NewFromConfig(s.base, func(o *cloudtrail.Options) {
		o.Region = t.region
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.cloudTrail)
	})
}

// cloudWatch returns a CloudWatch client for the call's account and
// Region.
func (s *Source) cloudWatch(t target) *cloudwatch.Client {
	return cloudwatch.NewFromConfig(s.base, func(o *cloudwatch.Options) {
		o.Region = t.region
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.cloudWatch)
	})
}

// eks returns an EKS client for the call's account and Region.
func (s *Source) eks(t target) *eks.Client {
	return eks.NewFromConfig(s.base, func(o *eks.Options) {
		o.Region = t.region
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.eks)
	})
}

// ecs returns an ECS client for the call's account and Region.
func (s *Source) ecs(t target) *ecs.Client {
	return ecs.NewFromConfig(s.base, func(o *ecs.Options) {
		o.Region = t.region
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.ecs)
	})
}

// health returns an AWS Health client for the call's account. The Health
// API is global: it answers in one Region of each partition, for every
// Region.
func (s *Source) health(t target) *health.Client {
	return health.NewFromConfig(s.base, func(o *health.Options) {
		o.Region = healthRegion(t.region)
		o.Credentials = t.account.creds
		o.BaseEndpoint = endpoint(s.apis.health)
	})
}

// newAccounts returns the accounts of the config by ID: an account with a
// role gets that role's credentials, assumed through client with
// Sidekick's own; any other gets Sidekick's own.
func newAccounts(accounts []Account, own awssdk.CredentialsProvider, client *sts.Client) map[string]*account {
	out := make(map[string]*account, len(accounts))

	for _, a := range accounts {
		acct := &account{id: a.ID, creds: own, own: true, sts: client}

		if a.RoleARN != "" {
			acct.creds = awsauth.AssumeRole(client, a.RoleARN, a.ExternalID, sessionName)
			acct.own = false
		}

		out[a.ID] = acct
	}

	return out
}

// withPlainBody returns req with a plainBody, as a copy, because a
// RoundTripper must not change the request it is given. A request without a
// body is returned as it is.
func withPlainBody(req *http.Request) *http.Request {
	if req.Body == nil || req.Body == http.NoBody {
		return req
	}

	out := req.WithContext(req.Context())
	out.Body = plainBody{body: req.Body}

	return out
}

// healthRegion returns the Region the AWS Health API of region's partition
// answers in.
func healthRegion(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "cn-northwest-1"
	case strings.HasPrefix(region, "us-gov-"):
		return "us-gov-west-1"
	default:
		return "us-east-1"
	}
}

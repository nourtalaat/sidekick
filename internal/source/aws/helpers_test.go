package aws

import (
	"context"
	"fmt"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Fake AWS credentials the tests sign with. No test calls AWS.
const (
	testAccessKey = "AKIATESTNOTREAL"
	testSecretKey = "test-secret-not-real"
)

// Test accounts, Regions and role.
const (
	testAccount  = "111122223333"
	roleAccount  = "444455556666"
	otherAccount = "999988887777"
	testRegion   = "us-east-1"
	otherRegion  = "eu-west-1"
	testRole     = "arn:aws:iam::" + roleAccount + ":role/sidekick-reader"
)

// Content types of AWS answers.
const (
	jsonType = "application/x-amz-json-1.1"
	xmlType  = "text/xml"
)

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// fakeAPIs are the fake AWS APIs of one test.
type fakeAPIs struct {
	logs       *fakehttp.Server
	cloudWatch *fakehttp.Server
	trail      *fakehttp.Server
	eks        *fakehttp.Server
	ecs        *fakehttp.Server
	health     *fakehttp.Server
	sts        *fakehttp.Server
}

// all returns every fake API, in the order requests lists their requests.
func (f fakeAPIs) all() []*fakehttp.Server {
	return []*fakehttp.Server{f.logs, f.cloudWatch, f.trail, f.eks, f.ecs, f.health, f.sts}
}

// requests returns the requests every fake API received, API by API in the
// order of all.
func (f fakeAPIs) requests() []fakehttp.Request {
	var out []fakehttp.Request
	for _, api := range f.all() {
		out = append(out, api.Requests()...)
	}

	return out
}

// newTestSource returns a source with every API on a fake server of the
// test's own, fake credentials and a fixed clock. It reads testAccount, with
// Sidekick's own credentials already checked, in testRegion, and the log
// groups under /ecs/ and /aws/eks/prod/. edit, when set, changes the config
// first.
func newTestSource(t *testing.T, edit func(*Config)) (*Source, fakeAPIs) {
	t.Helper()

	apis := fakeAPIs{logs: fakehttp.New(t), cloudWatch: fakehttp.New(t), trail: fakehttp.New(t), eks: fakehttp.New(t),
		ecs: fakehttp.New(t), health: fakehttp.New(t), sts: fakehttp.New(t)}
	for _, api := range apis.all() {
		api.ExpectAllCalled()
	}

	cfg := Config{
		Accounts:  []Account{{ID: testAccount}},
		Regions:   []string{testRegion},
		LogGroups: []string{"/ecs/", "/aws/eks/prod/"},
	}

	if edit != nil {
		edit(&cfg)
	}

	s, err := build(t.Context(), "aws", cfg, registry.Deps{HTTP: apis.logs.Client()}, fakeAWSConfig,
		endpoints{logs: apis.logs.URL(), cloudWatch: apis.cloudWatch.URL(), cloudTrail: apis.trail.URL(), eks: apis.eks.URL(),
			ecs: apis.ecs.URL(), health: apis.health.URL(), sts: apis.sts.URL()})
	require.NoError(t, err)

	s.now = func() time.Time { return testNow }
	s.pollEvery = time.Millisecond

	if a, ok := s.accounts[testAccount]; ok {
		a.checked = true
	}

	return s, apis
}

// fakeAWSConfig is an AWS config loader that applies the options it is
// given to an empty config with fake credentials and no retries. It reads
// no environment variables and no files, so a developer's own AWS setup
// never matters.
func fakeAWSConfig(_ context.Context, optFns ...func(*config.LoadOptions) error) (awssdk.Config, error) {
	var o config.LoadOptions
	for _, fn := range optFns {
		if err := fn(&o); err != nil {
			return awssdk.Config{}, err
		}
	}

	return awssdk.Config{
		Region:      o.Region,
		HTTPClient:  o.HTTPClient,
		Credentials: credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
		Retryer:     func() awssdk.Retryer { return awssdk.NopRetryer{} },
	}, nil
}

// answer returns a JSON answer of CloudWatch Logs or CloudTrail.
func answer(body string) fakehttp.Response {
	return fakehttp.Response{Headers: map[string]string{"Content-Type": jsonType}, Body: []byte(body)}
}

// refused returns a JSON error answer of CloudWatch Logs or CloudTrail.
func refused(status int, code, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  status,
		Headers: map[string]string{"Content-Type": jsonType, "X-Amzn-Errortype": code},
		Body:    fmt.Appendf(nil, `{"__type":%q,"message":%q}`, code, message),
	}
}

// xmlAnswer returns an XML answer of STS.
func xmlAnswer(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: map[string]string{"Content-Type": xmlType}, Body: []byte(body)}
}

// callerIdentity returns the STS GetCallerIdentity answer for account.
func callerIdentity(account string) fakehttp.Response {
	return xmlAnswer(0, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
    <Arn>arn:aws:sts::`+account+`:assumed-role/sidekick/i-1</Arn>
    <UserId>AROATESTNOTREAL:i-1</UserId>
    <Account>`+account+`</Account>
  </GetCallerIdentityResult>
  <ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`)
}

// assumedRole is the STS AssumeRole answer for testRole, with credentials
// that expire long after any test runs.
var assumedRole = xmlAnswer(0, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAASSUMEDNOTREAL</AccessKeyId>
      <SecretAccessKey>assumed-secret-not-real</SecretAccessKey>
      <SessionToken>assumed-session-token</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>arn:aws:sts::`+roleAccount+`:assumed-role/sidekick-reader/sidekick</Arn>
      <AssumedRoleId>AROATESTNOTREAL:sidekick</AssumedRoleId>
    </AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>r-2</RequestId></ResponseMetadata>
</AssumeRoleResponse>`)

// stsDenied is an STS answer that refuses the call.
var stsDenied = xmlAnswer(403, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized to perform sts:AssumeRole</Message></Error>
  <RequestId>r-3</RequestId>
</ErrorResponse>`)

// at returns a time on the test day.
func at(hour, minute int) time.Time {
	return time.Date(2026, 5, 4, hour, minute, 0, 0, time.UTC)
}

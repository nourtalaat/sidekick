// Package awsactiontest is what the tests of AWS actions share: an AWS config
// loader with fake credentials, and the answer a fake STS server gives to
// AssumeRole. No test that uses it calls AWS.
package awsactiontest

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// Fake AWS credentials the tests sign with.
const (
	accessKey = "AKIATESTNOTREAL"
	secretKey = "test-secret-not-real"
)

// xmlType is the content type of STS answers.
const xmlType = "text/xml"

// Loader is an awsauth.Loader that applies the options it is given to an
// empty config with fake credentials and no retries. It reads no environment
// variables and no files, so a developer's own AWS setup never matters.
func Loader(_ context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	var o config.LoadOptions

	for _, fn := range optFns {
		if err := fn(&o); err != nil {
			return aws.Config{}, fmt.Errorf("apply a load option: %w", err)
		}
	}

	return aws.Config{
		Region:      o.Region,
		HTTPClient:  o.HTTPClient,
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}, nil
}

// AssumedRole is the STS AssumeRole answer for a role of account, with
// credentials that expire long after any test runs.
func AssumedRole(account string) fakehttp.Response {
	return xml(0, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAASSUMEDNOTREAL</AccessKeyId>
      <SecretAccessKey>assumed-secret-not-real</SecretAccessKey>
      <SessionToken>assumed-session-token</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>arn:aws:sts::`+account+`:assumed-role/sidekick-action/sidekick</Arn>
      <AssumedRoleId>AROATESTNOTREAL:sidekick</AssumedRoleId>
    </AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata>
</AssumeRoleResponse>`)
}

// AssumeRoleDenied is the STS answer that refuses AssumeRole.
func AssumeRoleDenied() fakehttp.Response {
	return xml(403, `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized to perform sts:AssumeRole</Message></Error>
  <RequestId>r-2</RequestId>
</ErrorResponse>`)
}

// xml returns an XML answer of STS.
func xml(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: map[string]string{"Content-Type": xmlType}, Body: []byte(body)}
}

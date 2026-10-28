package awsauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// Fake AWS credentials the tests sign with. No test calls AWS.
const (
	testAccessKey = "AKIATESTNOTREAL"
	testSecretKey = "test-secret-not-real"
)

// assumeRoleAnswer is an STS AssumeRole answer with credentials that expire
// long after any test runs.
const assumeRoleAnswer = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <AssumeRoleResult>
    <Credentials>
      <AccessKeyId>ASIAASSUMEDNOTREAL</AccessKeyId>
      <SecretAccessKey>assumed-secret-not-real</SecretAccessKey>
      <SessionToken>assumed-session-token</SessionToken>
      <Expiration>2099-01-01T00:00:00Z</Expiration>
    </Credentials>
    <AssumedRoleUser>
      <Arn>arn:aws:sts::444455556666:assumed-role/sidekick-reader/sidekick</Arn>
      <AssumedRoleId>AROATESTNOTREAL:sidekick</AssumedRoleId>
    </AssumedRoleUser>
  </AssumeRoleResult>
  <ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata>
</AssumeRoleResponse>`

func TestLoad(t *testing.T) {
	t.Parallel()

	errProfile := errors.New("no such profile")
	static := credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, "")

	tests := []struct {
		name    string
		cfg     aws.Config
		err     error
		wantErr error
	}{
		{name: "credentials from the chain", cfg: aws.Config{Credentials: static}},
		{name: "the config cannot be loaded", err: errProfile, wantErr: errProfile},
		{name: "no credential provider", cfg: aws.Config{}, wantErr: ErrNoCredentials},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			httpClient := &http.Client{}

			var got config.LoadOptions

			load := func(_ context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
				for _, fn := range optFns {
					require.NoError(t, fn(&got))
				}

				return tt.cfg, tt.err
			}

			cfg, err := Load(t.Context(), load, "eu-west-2", httpClient)
			require.Equal(t, "eu-west-2", got.Region)
			require.Same(t, httpClient, got.HTTPClient)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.cfg.Credentials, cfg.Credentials)
		})
	}
}

func TestAssumeRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		externalID string
		want       url.Values
	}{
		{
			name:       "with an external ID",
			externalID: "sidekick-external",
			want: url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::444455556666:role/sidekick-reader"},
				"RoleSessionName": {"sidekick"}, "DurationSeconds": {"900"}, "ExternalId": {"sidekick-external"}},
		},
		{
			name: "without an external ID",
			want: url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {"arn:aws:iam::444455556666:role/sidekick-reader"},
				"RoleSessionName": {"sidekick"}, "DurationSeconds": {"900"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, "/", fakehttp.Response{Headers: map[string]string{"Content-Type": "text/xml"}, Body: []byte(assumeRoleAnswer)})
			api.ExpectAllCalled()

			client := sts.New(sts.Options{
				Region:       "us-east-1",
				Credentials:  credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
				BaseEndpoint: aws.String(api.URL()),
				HTTPClient:   api.Client(),
				Retryer:      aws.NopRetryer{},
			})

			provider := AssumeRole(client, "arn:aws:iam::444455556666:role/sidekick-reader", tt.externalID, "sidekick")

			creds, err := provider.Retrieve(t.Context())
			require.NoError(t, err)
			require.Equal(t, "ASIAASSUMEDNOTREAL", creds.AccessKeyID)
			require.Equal(t, "assumed-session-token", creds.SessionToken)

			// A second read uses the kept credentials and sends nothing.
			_, err = provider.Retrieve(t.Context())
			require.NoError(t, err)

			sent := api.Requests()
			require.Len(t, sent, 1)

			form, err := url.ParseQuery(string(sent[0].Body))
			require.NoError(t, err)
			require.Equal(t, tt.want, form)
		})
	}
}

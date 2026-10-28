// Package awsauth sets up AWS credentials for the plugins that call AWS: the
// Bedrock model connector and the AWS source.
//
// Credentials come from the default AWS chain: IRSA or EKS Pod Identity on
// EKS, otherwise environment variables, the shared config files or the
// instance role. They are read again whenever they expire, so rotated
// credentials are used without a restart. A plugin that reads more than one
// account assumes a role in each with AssumeRole.
package awsauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
)

// ErrNoCredentials is returned by Load when the AWS config has no
// credential provider at all.
var ErrNoCredentials = errors.New("the AWS config has no credential provider")

// Loader loads the AWS config, as config.LoadDefaultConfig does. Tests give
// one with fixed credentials, so they never read the environment or call
// AWS.
type Loader func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error)

// Load loads the AWS config for region, with httpClient for every request,
// through load, or config.LoadDefaultConfig when load is nil. It makes no
// network call: credentials are first read when a request needs them.
func Load(ctx context.Context, load Loader, region string, httpClient aws.HTTPClient) (aws.Config, error) {
	if load == nil {
		load = config.LoadDefaultConfig
	}

	cfg, err := load(ctx, config.WithRegion(region), config.WithHTTPClient(httpClient))
	if err != nil {
		return aws.Config{}, fmt.Errorf("load the AWS config: %w", err)
	}

	if cfg.Credentials == nil {
		return aws.Config{}, ErrNoCredentials
	}

	return cfg, nil
}

// AssumeRole returns the credentials of the IAM role roleARN, assumed through
// client with the session name sessionName and, when it is not empty, the
// external ID the role's trust policy asks for. They are kept until shortly
// before they expire, then assumed again.
func AssumeRole(client stscreds.AssumeRoleAPIClient, roleARN, externalID, sessionName string) aws.CredentialsProvider {
	return aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(client, roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = sessionName

		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	}))
}

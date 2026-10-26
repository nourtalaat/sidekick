package bedrock

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestNew(t *testing.T) {
	t.Parallel()

	errLoad := errors.New("no such profile")

	tests := []struct {
		name    string
		cfg     Config
		load    awsConfigLoader
		wantErr error
	}{
		{
			name: "known model",
			cfg:  Config{Settings: model.Settings{Model: opus55Geo}, Region: testRegion},
			load: fakeAWSConfig,
		},
		{
			name:    "unknown model without capabilities",
			cfg:     Config{Settings: model.Settings{Model: "anthropic.claude-unknown"}, Region: testRegion},
			load:    fakeAWSConfig,
			wantErr: errUnknownModel,
		},
		{
			name: "the AWS config cannot be loaded",
			cfg:  Config{Settings: model.Settings{Model: opus55Geo}, Region: testRegion},
			load: func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error) {
				return aws.Config{}, errLoad
			},
			wantErr: errLoad,
		},
		{
			name: "the AWS config has no credential provider",
			cfg:  Config{Settings: model.Settings{Model: opus55Geo}, Region: testRegion},
			load: func(context.Context, ...func(*config.LoadOptions) error) (aws.Config, error) {
				return aws.Config{Region: testRegion}, nil
			},
			wantErr: errNoCredentials,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := build(t.Context(), "claude", tt.cfg, registry.Deps{}, tt.load)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, knownModels[baseModelID(tt.cfg.Model)].caps, c.Capabilities())
			require.Equal(t, tt.cfg.Model, c.ModelID())
		})
	}
}

// TestNewLoadsTheRegion checks that the configured Region reaches the AWS
// config loader.
func TestNewLoadsTheRegion(t *testing.T) {
	t.Parallel()

	var got string

	load := func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		cfg, err := fakeAWSConfig(ctx, optFns...)
		got = cfg.Region

		return cfg, err
	}

	_, err := build(t.Context(), "claude", Config{Settings: model.Settings{Model: opus55Geo}, Region: "eu-west-2"}, registry.Deps{}, load)
	require.NoError(t, err)
	require.Equal(t, "eu-west-2", got)
}

// TestGenerateWithoutCredentials checks that credentials that cannot be
// read are an auth error, and that nothing is sent.
func TestGenerateWithoutCredentials(t *testing.T) {
	t.Parallel()

	api := serve(t, opus55Geo)
	load := func(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
		cfg, err := fakeAWSConfig(ctx, optFns...)
		cfg.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, errors.New("no web identity token file")
		})

		return cfg, err
	}

	cfg := Config{Settings: model.Settings{Model: opus55Geo}, Region: testRegion, Endpoint: api.URL()}

	c, err := build(t.Context(), "claude", cfg, registry.Deps{HTTP: api.Client()}, load)
	require.NoError(t, err)

	_, err = c.Generate(t.Context(), userText("hi"))

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrAuth, merr.Kind)
	require.ErrorContains(t, err, "connector claude: get AWS credentials")
	require.Empty(t, api.Requests())
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		section  string
		wantCaps model.Capabilities
		wantErr  string
	}{
		{
			name:     "a known model declares its capabilities",
			section:  "model: global.anthropic.claude-opus-5-5\nregion: us-east-1\neffort: high\n",
			wantCaps: knownModels["anthropic.claude-opus-5-5"].caps,
		},
		{
			name:    "effort none is rejected",
			section: "model: global.anthropic.claude-opus-5-5\nregion: us-east-1\neffort: none\n",
			wantErr: `effort: "none" is not supported by global.anthropic.claude-opus-5-5`,
		},
		{
			name:    "region is required",
			section: "model: global.anthropic.claude-opus-5-5\n",
			wantErr: `region: "" is not an AWS Region code`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			decoded, err := registry.Models.Check(t.Context(), Provider, "claude", node.Content[0])
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			declarer, ok := decoded.(model.CapabilityDeclarer)
			require.True(t, ok)
			require.Equal(t, tt.wantCaps, declarer.Capabilities())
			require.NoError(t, model.CheckRole(model.RoleInvestigate, declarer.Capabilities()))
		})
	}
}

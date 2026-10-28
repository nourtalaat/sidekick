package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Paths of Azure's v1 API under the fake server.
const (
	azureResponsesPath = "/openai/v1/responses"
	azureChatPath      = "/openai/v1/chat/completions"
	azureModelsPath    = "/openai/v1/models"
)

// testDeployment is the deployment name the Azure tests send.
const testDeployment = "prod-gpt"

// testEntraToken is the fake Entra ID token the tests send.
const testEntraToken = "test-entra-token-not-real"

// fixedEntraToken is an Entra ID credential that always gives the same
// token, or err, and records the scopes it was asked for.
type fixedEntraToken struct {
	err    error
	scopes chan []string
}

// GetToken implements azcore.TokenCredential.
func (f fixedEntraToken) GetToken(_ context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if f.scopes != nil {
		select {
		case f.scopes <- opts.Scopes:
		default:
		}
	}

	if f.err != nil {
		return azcore.AccessToken{}, f.err
	}

	return azcore.AccessToken{Token: testEntraToken, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestAzureGenerate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      AzureConfig
		path     string
		response recorded
		// wantHeaders are request headers the call must send; an empty
		// value means the header must be absent.
		wantHeaders map[string]string
		// wantBody holds top-level fields of the request body.
		wantBody       map[string]string
		absent         []string
		wantRequestID  string
		wantKind       model.ErrorKind
		wantRetryAfter time.Duration
	}{
		{
			name:          "responses with an API key",
			cfg:           azureKeyConfig("gpt-6.1-sol"),
			path:          azureResponsesPath,
			response:      recorded{status: http.StatusOK, file: "responses_text.sse", headers: map[string]string{apimRequestID: "0f5c3a2e-apim"}},
			wantHeaders:   map[string]string{azureKeyHeader: testAPIKey, "Authorization": ""},
			wantBody:      map[string]string{"model": `"prod-gpt"`, "store": `false`},
			wantRequestID: "0f5c3a2e-apim",
		},
		{
			name:        "responses with Entra ID",
			cfg:         azureEntraConfig("gpt-6.1-sol"),
			path:        azureResponsesPath,
			response:    recorded{status: http.StatusOK, file: "responses_text.sse"},
			wantHeaders: map[string]string{"Authorization": "Bearer " + testEntraToken, azureKeyHeader: ""},
		},
		{
			name: "chat completions take max_completion_tokens",
			cfg: changedAzure(azureKeyConfig("gpt-5.4"), func(c *AzureConfig) {
				c.API = APIChatCompletions
				c.MaxTokens = 2000
			}),
			path:     azureChatPath,
			response: recorded{status: http.StatusOK, file: "chat_text.sse"},
			wantBody: map[string]string{"model": `"prod-gpt"`, "max_completion_tokens": `2000`},
			absent:   []string{"max_tokens"},
		},
		{
			name:     "Foundry model on chat completions with max_tokens",
			cfg:      changedAzure(azureKeyConfig("DeepSeek-V4-Flash"), func(c *AzureConfig) { c.MaxTokens = 4000 }),
			path:     azureChatPath,
			response: recorded{status: http.StatusOK, file: "chat_text.sse"},
			wantBody: map[string]string{"model": `"prod-gpt"`, "max_tokens": `4000`},
			absent:   []string{"max_completion_tokens", "tool_choice"},
		},
		{
			name:     "deployment named after the model",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Deployment = "" }),
			path:     azureResponsesPath,
			response: recorded{status: http.StatusOK, file: "responses_text.sse"},
			wantBody: map[string]string{"model": `"gpt-6.1-sol"`},
		},
		{
			name:     "prompt blocked by the content filter",
			cfg:      azureKeyConfig("gpt-6.1-sol"),
			path:     azureResponsesPath,
			response: recorded{status: http.StatusBadRequest, file: "azure_content_filter.json"},
			wantKind: model.ErrInvalid,
		},
		{
			name:     "deployment that does not exist",
			cfg:      azureKeyConfig("gpt-6.1-sol"),
			path:     azureResponsesPath,
			response: recorded{status: http.StatusNotFound, file: "azure_deployment_not_found.json"},
			wantKind: model.ErrInvalid,
		},
		{
			name: "rate limited with retry-after-ms",
			cfg:  azureKeyConfig("gpt-6.1-sol"),
			path: azureResponsesPath,
			response: recorded{
				status:  http.StatusTooManyRequests,
				file:    "azure_rate_limit.json",
				headers: map[string]string{model.RetryAfterHeader: "6", model.RetryAfterMSHeader: "5800"},
			},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 5800 * time.Millisecond,
		},
		{
			name:     "bad key",
			cfg:      azureKeyConfig("gpt-6.1-sol"),
			path:     azureResponsesPath,
			response: recorded{status: http.StatusUnauthorized, file: "azure_bad_key.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "identity without the role",
			cfg:      azureEntraConfig("gpt-6.1-sol"),
			path:     azureResponsesPath,
			response: recorded{status: http.StatusForbidden, file: "azure_permission_denied.json"},
			wantKind: model.ErrAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.path, tt.response)
			c := newAzureTestConnector(t, api, tt.cfg)

			got, err := c.Generate(t.Context(), userText("Why is checkout failing?"))
			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, tt.wantRetryAfter)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantRequestID, got.RequestID)

			sent := api.Requests()[0]
			for name, want := range tt.wantHeaders {
				require.Equal(t, want, sent.Header.Get(name), name)
			}

			body := requestBody(t, api, 0)
			for field, want := range tt.wantBody {
				require.JSONEq(t, want, string(body[field]), field)
			}

			for _, field := range tt.absent {
				require.NotContains(t, body, field)
			}
		})
	}
}

// TestAzureReasoning checks that reasoning items are kept as azure
// reasoning and that only azure reasoning is sent back: OpenAI's encrypted
// reasoning cannot be read by Azure, and the other way round.
func TestAzureReasoning(t *testing.T) {
	t.Parallel()

	api := serve(t, azureResponsesPath,
		recorded{status: http.StatusOK, file: "responses_tool_call.sse"},
		recorded{status: http.StatusOK, file: "responses_after_tools.sse"},
	)
	c := newAzureTestConnector(t, api, azureKeyConfig("gpt-6.1-sol"))

	req := userText("Why is checkout failing?")

	first, err := c.Generate(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, model.Reasoning{Provider: AzureProvider, ID: recordedReasoningID, Text: recordedSummary, Signature: recordedEncryptedReasoning}, first.Message.Parts[0])

	assistant := first.Message
	assistant.Parts = append([]model.Part{model.Reasoning{Provider: Provider, ID: "rs_fromOpenAI", Signature: "openai-only"}}, assistant.Parts...)

	req.Messages = append(req.Messages, assistant, model.Message{Role: model.User, Parts: []model.Part{
		model.ToolResult{CallID: "call_01Deploys", Content: "deploy 41 at 13:40"},
		model.ToolResult{CallID: "call_02Logs", Content: "no errors"},
	}})

	_, err = c.Generate(t.Context(), req)
	require.NoError(t, err)

	var input []map[string]any
	require.NoError(t, json.Unmarshal(requestBody(t, api, 1)["input"], &input))

	var reasoningIDs []any

	for _, item := range input {
		if item["type"] == itemReasoning {
			reasoningIDs = append(reasoningIDs, item["id"])
		}
	}

	require.Equal(t, []any{recordedReasoningID}, reasoningIDs)
}

// TestAzureScope checks the scope Entra ID tokens are asked for.
func TestAzureScope(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	scopes := make(chan []string, 1)

	cfg := azureEntraConfig("gpt-6.1-sol")
	cfg.Endpoint = api.URL()

	_, err := buildAzure(t.Context(), "azure", cfg, registry.Deps{HTTP: api.Client()}, func(*http.Client) (azcore.TokenCredential, error) {
		return fixedEntraToken{scopes: scopes}, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{entraScope}, <-scopes)
}

func TestAzureCheckKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      AzureConfig
		status   int
		file     string
		wantKind model.ErrorKind
	}{
		{name: "valid key", cfg: azureKeyConfig("gpt-6.1-sol"), status: http.StatusOK, file: "azure_models.json"},
		{name: "valid token", cfg: azureEntraConfig("gpt-6.1-sol"), status: http.StatusOK, file: "azure_models.json"},
		{name: "rejected key", cfg: azureKeyConfig("gpt-6.1-sol"), status: http.StatusUnauthorized, file: "azure_bad_key.json", wantKind: model.ErrAuth},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, azureModelsPath, fakehttp.Response{
				Status:   tt.status,
				Headers:  map[string]string{"Content-Type": contentTypeJSON},
				BodyFile: "fixture/" + tt.file,
			})
			api.ExpectAllCalled()

			c := newAzureTestConnector(t, api, tt.cfg)

			err := c.CheckKey(t.Context())
			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, 0)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestAzureCredentialErrors checks that credentials that cannot be read at
// call time are an auth error and send nothing.
func TestAzureCredentialErrors(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1"), nil),
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return(nil, errors.New("gone")),
	)

	cfg := azureKeyConfig("gpt-6.1-sol")
	cfg.Endpoint = api.URL()

	c, err := buildAzure(t.Context(), "azure", cfg, registry.Deps{Secrets: s, HTTP: api.Client()}, nil)
	require.NoError(t, err)

	_, err = c.Generate(t.Context(), userText("hi"))
	requireKind(t, err, model.ErrAuth, 0)
	require.Empty(t, api.Requests())
}

func TestNewAzure(t *testing.T) {
	t.Parallel()

	errNoIdentity := errors.New("no Azure identity found")

	tests := []struct {
		name string
		cfg  AzureConfig
		// noSecrets builds the connector without a secrets resolver.
		noSecrets bool
		// secretErr is what resolving the key returns.
		secretErr error
		// credErr is what making the Entra ID credential returns, and
		// tokenErr what getting a token returns.
		credErr  error
		tokenErr error
		wantErr  error
	}{
		{name: "API key", cfg: azureKeyConfig("gpt-6.1-sol")},
		{name: "Entra ID", cfg: azureEntraConfig("gpt-6.1-sol"), noSecrets: true},
		{name: "key that cannot be read", cfg: azureKeyConfig("gpt-6.1-sol"), secretErr: fs.ErrNotExist, wantErr: fs.ErrNotExist},
		{name: "key without secrets", cfg: azureKeyConfig("gpt-6.1-sol"), noSecrets: true, wantErr: errNoSecrets},
		{name: "no Entra ID credential", cfg: azureEntraConfig("gpt-6.1-sol"), credErr: errNoIdentity, wantErr: errNoIdentity},
		{name: "no Entra ID token", cfg: azureEntraConfig("gpt-6.1-sol"), tokenErr: errNoIdentity, wantErr: errNoIdentity},
		{name: "unknown model", cfg: azureKeyConfig("llama-4-maverick"), wantErr: errUnknownModel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			s := registry.NewMockSecrets(ctrl)

			if tt.cfg.Auth != AzureAuthEntra && !tt.noSecrets && !errors.Is(tt.wantErr, errUnknownModel) {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey), tt.secretErr)
			}

			deps := registry.Deps{Secrets: s}
			if tt.noSecrets {
				deps.Secrets = nil
			}

			makeCredential := func(*http.Client) (azcore.TokenCredential, error) {
				if tt.credErr != nil {
					return nil, tt.credErr
				}

				return fixedEntraToken{err: tt.tokenErr}, nil
			}

			c, err := buildAzure(t.Context(), "azure", tt.cfg, deps, makeCredential)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.cfg.Model, c.ModelID())
		})
	}
}

func TestAzureConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  AzureConfig
		// wantErrs are parts of the error message; none means valid.
		wantErrs []string
	}{
		{name: "API key", cfg: azureKeyConfig("gpt-6.1-sol")},
		{name: "Entra ID", cfg: azureEntraConfig("gpt-6.1-sol")},
		{name: "Foundry endpoint", cfg: changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "https://my-resource.services.ai.azure.com" })},
		{name: "endpoint with the v1 path", cfg: changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "https://my-resource.openai.azure.com/openai/v1/" })},
		{name: "http on loopback", cfg: changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "http://127.0.0.1:8080" })},
		{
			name:     "no endpoint",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "" }),
			wantErrs: []string{"endpoint: required"},
		},
		{
			name:     "http to another host",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "http://my-resource.openai.azure.com" }),
			wantErrs: []string{"must use https"},
		},
		{
			name:     "old deployment path",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Endpoint = "https://my-resource.openai.azure.com/openai/deployments/prod-gpt" }),
			wantErrs: []string{"must be the resource address"},
		},
		{
			name: "api-version",
			cfg: changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) {
				c.Endpoint = "https://my-resource.openai.azure.com?api-version=2025-04-01-preview"
			}),
			wantErrs: []string{"must not have a query"},
		},
		{
			name:     "deployment name that is not one",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Deployment = "prod/gpt" }),
			wantErrs: []string{`deployment: "prod/gpt" is not a deployment name`},
		},
		{
			name:     "unknown API",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.API = "assistants" }),
			wantErrs: []string{`api: "assistants" is not an API`},
		},
		{
			name:     "no key",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.APIKeySecret = registry.SecretRef{} }),
			wantErrs: []string{"apiKeySecret: required unless auth is entra"},
		},
		{
			name:     "key with Entra ID",
			cfg:      changedAzure(azureEntraConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.APIKeySecret = keyRef }),
			wantErrs: []string{"apiKeySecret: not accepted with auth entra"},
		},
		{
			name:     "unknown auth",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Auth = "sas" }),
			wantErrs: []string{`auth: "sas" is not an auth kind; use apiKey or entra`},
		},
		{
			name:     "unknown model",
			cfg:      azureKeyConfig("llama-4-maverick"),
			wantErrs: []string{`model: "llama-4-maverick" is not a model the azure provider knows`},
		},
		{name: "no model", cfg: azureKeyConfig(""), wantErrs: []string{"model: required"}},
		{name: "Foundry model", cfg: azureKeyConfig("DeepSeek-V4-Pro")},
		{
			name:     "effort on a Foundry model",
			cfg:      changedAzure(azureKeyConfig("grok-4.7"), func(c *AzureConfig) { c.Effort = model.EffortHigh }),
			wantErrs: []string{`effort: "high" is not supported by grok-4.7`},
		},
		{
			name: "declared model",
			cfg: changedAzure(azureKeyConfig("my-fine-tune"), func(c *AzureConfig) {
				c.Declared = &DeclaredCapabilities{ToolCalling: true, ToolChoice: true, EffortLevels: []model.Effort{model.EffortLow}}
				c.Effort = model.EffortLow
			}),
		},
		{
			name:     "effort the model lacks",
			cfg:      changedAzure(azureKeyConfig("gpt-5.4"), func(c *AzureConfig) { c.Effort = model.EffortMax }),
			wantErrs: []string{`effort: "max" is not supported by gpt-5.4`},
		},
		{
			name:     "thinking",
			cfg:      changedAzure(azureKeyConfig("gpt-6.1-sol"), func(c *AzureConfig) { c.Thinking = model.Thinking{Mode: model.ThinkingAdaptive} }),
			wantErrs: []string{"thinking: not accepted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// TestAzureForcedToolChoiceOnFoundry checks that a Foundry model, which
// is not known to honour a forced tool choice, rejects one before sending.
func TestAzureForcedToolChoiceOnFoundry(t *testing.T) {
	t.Parallel()

	api := serve(t, azureChatPath)
	c := newAzureTestConnector(t, api, azureKeyConfig("Kimi-K2.6"))

	req := userText("hi")
	req.ToolChoice = model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"}

	_, err := c.Generate(t.Context(), req)
	requireKind(t, err, model.ErrInvalid, 0)
	require.ErrorIs(t, err, errForcedToolChoice)
	require.Empty(t, api.Requests())
}

func TestAzureAPI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  AzureConfig
		want API
	}{
		{name: "OpenAI model", cfg: azureKeyConfig("gpt-6.1-sol"), want: APIResponses},
		{name: "Foundry model", cfg: azureKeyConfig("grok-4.7"), want: APIChatCompletions},
		{
			name: "declared model",
			cfg:  changedAzure(azureKeyConfig("my-model"), func(c *AzureConfig) { c.Declared = &DeclaredCapabilities{ToolCalling: true} }),
			want: APIChatCompletions,
		},
		{name: "set", cfg: changedAzure(azureKeyConfig("grok-4.7"), func(c *AzureConfig) { c.API = APIResponses }), want: APIResponses},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.cfg.api())
		})
	}
}

// TestFoundryModels checks the table: no Foundry model is assumed to
// honour a forced tool choice, and only the DeepSeek models, whose tool
// call turns need reasoning sent back, cannot be the investigate model.
func TestFoundryModels(t *testing.T) {
	t.Parallel()

	for id, caps := range foundryModels {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			if strings.HasPrefix(id, "DeepSeek-") {
				require.ErrorIs(t, model.CheckRole(model.RoleInvestigate, caps), model.ErrNoToolCalling)
			} else {
				require.NoError(t, model.CheckRole(model.RoleInvestigate, caps))
			}

			require.False(t, caps.ToolChoice)
			require.Positive(t, caps.ContextTokens)
			require.Positive(t, caps.MaxOutputTokens)
			require.NotContains(t, knownModels, id)
		})
	}
}

func TestAzureBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "resource", endpoint: "https://my-resource.openai.azure.com", want: "https://my-resource.openai.azure.com/openai/v1/"},
		{name: "resource with a slash", endpoint: "https://my-resource.services.ai.azure.com/", want: "https://my-resource.services.ai.azure.com/openai/v1/"},
		{name: "v1 path", endpoint: "https://my-resource.openai.azure.com/openai/v1", want: "https://my-resource.openai.azure.com/openai/v1/"},
		{name: "v1 path with a slash", endpoint: "https://my-resource.openai.azure.com/openai/v1/", want: "https://my-resource.openai.azure.com/openai/v1/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := AzureConfig{Endpoint: tt.endpoint}
			require.Equal(t, tt.want, cfg.baseURL())
		})
	}
}

// TestAzureRegistered checks that the provider decodes and validates its
// section through the registry.
func TestAzureRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		wantErr string
	}{
		{name: "valid", section: "model: gpt-6.1-sol\nendpoint: https://my-resource.openai.azure.com\nauth: entra\n"},
		{name: "invalid", section: "model: gpt-6.1-sol\nendpoint: https://my-resource.openai.azure.com\n", wantErr: "apiKeySecret: required unless auth is entra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			_, err := registry.Models.Check(t.Context(), AzureProvider, "azure", node.Content[0])
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// newAzureTestConnector builds an Azure connector from cfg that sends its
// requests to api, with its key read through a mock or a fixed Entra ID
// token.
func newAzureTestConnector(t *testing.T, api *fakehttp.Server, cfg AzureConfig) *AzureConnector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	cfg.Endpoint = api.URL()

	c, err := buildAzure(t.Context(), "azure", cfg, registry.Deps{Secrets: s, HTTP: api.Client()}, func(*http.Client) (azcore.TokenCredential, error) {
		return fixedEntraToken{}, nil
	})
	require.NoError(t, err)

	return c
}

// azureKeyConfig is an Azure connector for modelID on a deployment, with
// an API key.
func azureKeyConfig(modelID string) AzureConfig {
	return AzureConfig{
		Settings:     model.Settings{Model: modelID},
		Endpoint:     "https://my-resource.openai.azure.com",
		Deployment:   testDeployment,
		APIKeySecret: keyRef,
	}
}

// azureEntraConfig is an Azure connector for modelID on a deployment, with
// Entra ID.
func azureEntraConfig(modelID string) AzureConfig {
	cfg := azureKeyConfig(modelID)
	cfg.APIKeySecret = registry.SecretRef{}
	cfg.Auth = AzureAuthEntra

	return cfg
}

// changedAzure returns cfg after change.
func changedAzure(cfg AzureConfig, change func(c *AzureConfig)) AzureConfig {
	change(&cfg)

	return cfg
}

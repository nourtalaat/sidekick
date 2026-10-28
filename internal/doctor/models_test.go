package doctor

import (
	"context"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// jsonHeaders mark a fake response as JSON.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

func TestCheckModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		noKey     bool
		responses []fakehttp.Response
		want      Result
		// wantPrefix is set when the message holds an error from the SDK
		// whose full text the test does not pin.
		wantPrefix string
	}{
		{
			name:      "the key works",
			responses: []fakehttp.Response{{BodyFile: filepath.Join("fixture", "anthropic-models.json"), Headers: jsonHeaders}},
			want:      Result{Check: "model claude", Status: StatusOK, Message: "the anthropic API accepted the key"},
		},
		{
			name:       "the key is rejected",
			responses:  []fakehttp.Response{{Status: http.StatusUnauthorized, BodyFile: filepath.Join("fixture", "anthropic-auth.json"), Headers: jsonHeaders}},
			want:       Result{Check: "model claude", Status: StatusFail, Fix: "put a valid anthropic API key in the Secret the connector's apiKeySecret names, and check the account's billing"},
			wantPrefix: "the API key was rejected or cannot be read: ",
		},
		{
			name:       "the API is overloaded",
			responses:  []fakehttp.Response{{Status: http.StatusServiceUnavailable, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), Headers: jsonHeaders}},
			want:       Result{Check: "model claude", Status: StatusFail, Fix: "check that Sidekick's pod can reach the anthropic API, through any network policy or proxy"},
			wantPrefix: "cannot check the key: ",
		},
		{
			name:       "the key secret is missing",
			noKey:      true,
			want:       Result{Check: "model claude", Status: StatusFail, Fix: "mount every secret the connector names; `sidekick config validate --check-secrets -f <file>` points at the field"},
			wantPrefix: "cannot set up the connector: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.responses) > 0 {
				api.Handle(http.MethodGet, "/v1/models", tt.responses...)
			}

			api.ExpectAllCalled()

			files := maps.Clone(defaultSecrets)
			if tt.noKey {
				delete(files, "anthropic/apiKey")
			}

			cfg := parseConfig(t, baseConfig, files, config.Registries{}, "ANTHROPIC_URL", api.URL())

			got := newDoctor(Options{Config: cfg, HTTP: api.Client()}).checkModels(t.Context())
			require.Len(t, got, 1)

			if tt.wantPrefix != "" {
				require.True(t, strings.HasPrefix(got[0].Message, tt.wantPrefix), got[0].Message)
				got[0].Message = ""
			}

			require.Equal(t, tt.want, got[0])

			for _, req := range api.Requests() {
				require.Contains(t, req.Header.Get("Authorization")+req.Header.Get("X-Api-Key"), testAPIKey)
			}
		})
	}
}

func TestCheckModelsWithoutKeyCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want []Result
	}{
		{
			name: "a provider without a key check is skipped",
			want: []Result{{Check: "model claude", Status: StatusSkip, Message: "the anthropic provider has no API key check"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			models := registry.New[model.Connector]("model")
			models.Register("anthropic", registry.Plugin[model.Connector]{
				Build: func(context.Context, string, registry.Decoder, registry.Deps) (model.Connector, error) {
					return plainConnector{}, nil
				},
			})

			regs := config.Registries{Models: models}
			cfg := parseConfig(t, baseConfig, defaultSecrets, regs)

			got := newDoctor(Options{Config: cfg, Registries: regs}).checkModels(t.Context())

			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckPrices(t *testing.T) {
	t.Parallel()

	// custom is a model the shipped price table does not have.
	const custom = `
    custom: { provider: anthropic, model: claude-custom-1, apiKeySecret: { name: anthropic, key: apiKey }, capabilities: { toolCalling: true, toolChoice: true } }
`

	tests := []struct {
		name   string
		config string
		want   []Result
	}{
		{
			name:   "every model has a price",
			config: baseConfig,
			want:   []Result{{Check: "prices", Status: StatusOK, Message: "every configured model has a price"}},
		},
		{
			name:   "a model without a price",
			config: strings.Replace(baseConfig, "  roles:", strings.TrimPrefix(custom, "\n")+"  roles:", 1),
			want: []Result{{
				Check: "price custom", Status: StatusWarn,
				Message: "the model claude-custom-1 has no price, so its calls show no cost and count nothing toward the cost budgets and daily caps",
				Fix:     `add model.prices."claude-custom-1" with inputPerMTok and outputPerMTok, in US dollars per million tokens`,
			}},
		},
		{
			name: "an embedding model with a price",
			config: baseConfig + `
memory:
  vector:
    enabled: true
    embeddings: { provider: openai, model: text-embedding-3-small, dimensions: 1536, apiKeySecret: { name: anthropic, key: apiKey } }
`,
			want: []Result{{Check: "prices", Status: StatusOK, Message: "every configured model has a price"}},
		},
		{
			name: "an embedding model without a price",
			config: baseConfig + `
memory:
  vector:
    enabled: true
    embeddings: { provider: openai, model: nomic-embed-text, dimensions: 768, baseURL: "http://127.0.0.1:11434/v1" }
`,
			want: []Result{{
				Check: "price memory-embeddings", Status: StatusWarn,
				Message: "the embedding model nomic-embed-text has no price, so its calls show no cost in the metrics",
				Fix:     `add model.prices."nomic-embed-text" with inputPerMTok, in US dollars per million tokens`,
			}},
		},
		{
			name:   "a price from config",
			config: strings.Replace(baseConfig, "  roles:", strings.TrimPrefix(custom, "\n")+"  prices:\n    claude-custom-1: { inputPerMTok: 1, outputPerMTok: 5 }\n  roles:", 1),
			want:   []Result{{Check: "prices", Status: StatusOK, Message: "every configured model has a price"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := parseConfig(t, tt.config, defaultSecrets, config.Registries{})

			got := newDoctor(Options{Config: cfg}).checkPrices(t.Context())

			require.Equal(t, tt.want, got)
		})
	}
}

// plainConnector is a connector that cannot check its key.
type plainConnector struct{}

func (plainConnector) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true, ToolChoice: true}
}

func (plainConnector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

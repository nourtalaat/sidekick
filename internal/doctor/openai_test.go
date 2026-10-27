package doctor

import (
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestCheckLocalModels(t *testing.T) {
	t.Parallel()

	const serverFix = "add the kind of server at baseURL to model.connectors.local: `server: vllm`, `server: llamacpp` or `server: ollama`"

	version := fakehttp.Response{BodyFile: filepath.Join("fixture", "ollama-version.json"), Headers: jsonHeaders}
	notFound := fakehttp.Response{Status: http.StatusNotFound}

	tests := []struct {
		name    string
		replace []string
		// routes maps a GET path of the fake server to its responses.
		routes map[string][]fakehttp.Response
		want   []Result
	}{
		{
			name:    "a preset with its server set",
			replace: []string{"LOCAL_MODEL", "Qwen/Qwen3.8-27B", "LOCAL_EXTRA", ", server: vllm"},
		},
		{
			name: "a known OpenAI model behind a gateway",
			replace: []string{
				"LOCAL_MODEL", "gpt-6.1-sol",
				"LOCAL_EXTRA", ", apiKeySecret: { name: anthropic, key: apiKey }",
			},
		},
		{
			name:    "a declared model without a server",
			replace: []string{"LOCAL_MODEL", "my-model", "LOCAL_EXTRA", ", capabilities: { toolCalling: true }"},
		},
		{
			name:    "no server set, and Ollama answers",
			replace: []string{"LOCAL_MODEL", "Qwen/Qwen3.8-27B"},
			routes:  map[string][]fakehttp.Response{"/api/version": {version}},
			want: []Result{{
				Check: "server local", Status: StatusWarn,
				Message: "no server set; the server at baseURL is Ollama 0.13.3",
				Fix:     "add `server: ollama` to model.connectors.local",
			}},
		},
		{
			name:    "no server set on a server that is not Ollama",
			replace: []string{"LOCAL_MODEL", "Qwen/Qwen3.8-27B"},
			routes:  map[string][]fakehttp.Response{"/api/version": {notFound}},
			want: []Result{{
				Check: "server local", Status: StatusWarn,
				Message: "no server set, so Sidekick assumes a server that ignores a forced tool choice; the server at baseURL did not answer as Ollama: /api/version answered 404 Not Found",
				Fix:     serverFix,
			}},
		},
		{
			name:   "no server set for an Ollama tag the server does not confirm",
			routes: map[string][]fakehttp.Response{"/api/version": {{Status: http.StatusOK, Body: []byte(`{}`), Headers: jsonHeaders}}},
			want: []Result{{
				Check: "server local", Status: StatusWarn,
				Message: "no server set, so Sidekick assumes Ollama from the model tag or the port; the server at baseURL did not answer as Ollama: /api/version answered without a version",
				Fix:     serverFix,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			for path, responses := range tt.routes {
				api.Handle(http.MethodGet, path, responses...)
			}

			api.ExpectAllCalled()

			cfg := parseConfig(t, localConfig, defaultSecrets, config.Registries{}, localReplacements(slices.Concat(tt.replace, []string{"LOCAL_URL", api.URL()})...)...)

			got := newDoctor(Options{Config: cfg, HTTP: api.Client()}).checkLocalModels(t.Context())

			require.Equal(t, tt.want, got)

			for _, req := range api.Requests() {
				require.Empty(t, req.Header.Get("Authorization"))
			}
		})
	}
}

func TestOllamaURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{name: "the usual /v1 address", baseURL: "http://ollama:11434/v1", want: "http://ollama:11434/api/version"},
		{name: "a trailing slash", baseURL: "http://ollama:11434/v1/", want: "http://ollama:11434/api/version"},
		{name: "no path", baseURL: "http://ollama:11434", want: "http://ollama:11434/api/version"},
		{name: "behind a path prefix", baseURL: "https://proxy.example/ollama/v1", want: "https://proxy.example/ollama/api/version"},
		{name: "user info and query are dropped", baseURL: "https://user:pass@proxy.example/v1?key=value", want: "https://proxy.example/api/version"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ollamaURL(tt.baseURL, ollamaVersionPath)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

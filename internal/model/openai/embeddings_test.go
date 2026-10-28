package openai

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// embeddingsPath is the embeddings endpoint under the fake server's /v1 base URL.
const embeddingsPath = "/v1/embeddings"

// embedConfig is an embedding connector for modelID with a key.
func embedConfig(modelID string, dimensions int) Config {
	return Config{Settings: model.Settings{Model: modelID}, APIKeySecret: keyRef, Dimensions: dimensions}
}

// jsonAnswer is a recorded JSON answer with status.
func jsonAnswer(status int, file string, headers map[string]string) recorded {
	h := map[string]string{"Content-Type": contentTypeJSON}
	for k, v := range headers {
		h[k] = v
	}

	return recorded{status: status, file: file, headers: h}
}

func TestEmbed(t *testing.T) {
	t.Parallel()

	texts := []string{"first alert", "second alert"}

	tests := []struct {
		name     string
		cfg      Config
		answer   recorded
		wantErr  error
		wantKind model.ErrorKind
		// wantRetryAfter is the wait the answer asks for.
		wantRetryAfter time.Duration
		// wantDimensions is the dimensions field the request must send, or
		// zero when it must not send one.
		wantDimensions int
	}{
		{
			name:           "known flexible model asks for the dimensions",
			cfg:            embedConfig("text-embedding-3-small", 3),
			answer:         jsonAnswer(http.StatusOK, "embeddings.json", map[string]string{"X-Request-Id": "req_emb_1"}),
			wantDimensions: 3,
		},
		{
			name:   "a server at baseURL that declares its dimensions is not asked for them",
			cfg:    embedConfig("nomic-embed-text", 3),
			answer: jsonAnswer(http.StatusOK, "embeddings.json", nil),
		},
		{
			name:     "vectors of the wrong length",
			cfg:      embedConfig("nomic-embed-text", 3),
			answer:   jsonAnswer(http.StatusOK, "embeddings_wrong_length.json", nil),
			wantErr:  errWrongVectorLength,
			wantKind: model.ErrInvalid,
		},
		{
			name:     "fewer vectors than texts",
			cfg:      embedConfig("nomic-embed-text", 3),
			answer:   jsonAnswer(http.StatusOK, "embeddings_missing.json", nil),
			wantErr:  errBadAnswer,
			wantKind: model.ErrInvalid,
		},
		{
			name:     "a repeated vector index",
			cfg:      embedConfig("nomic-embed-text", 3),
			answer:   jsonAnswer(http.StatusOK, "embeddings_repeated.json", nil),
			wantErr:  errBadAnswer,
			wantKind: model.ErrInvalid,
		},
		{
			name:           "rate limited with retry-after",
			cfg:            embedConfig("text-embedding-3-small", 3),
			answer:         jsonAnswer(http.StatusTooManyRequests, "embeddings_rate_limit.json", map[string]string{"Retry-After": "2"}),
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 2 * time.Second,
			wantDimensions: 3,
		},
		{
			name:           "bad key",
			cfg:            embedConfig("text-embedding-3-small", 3),
			answer:         jsonAnswer(http.StatusUnauthorized, "auth.json", nil),
			wantKind:       model.ErrAuth,
			wantDimensions: 3,
		},
		{
			name:           "server error",
			cfg:            embedConfig("text-embedding-3-small", 3),
			answer:         jsonAnswer(http.StatusInternalServerError, "server_error.json", nil),
			wantKind:       model.ErrOverloaded,
			wantDimensions: 3,
		},
		{
			name:           "invalid request",
			cfg:            embedConfig("text-embedding-3-small", 3),
			answer:         jsonAnswer(http.StatusBadRequest, "invalid.json", nil),
			wantKind:       model.ErrInvalid,
			wantDimensions: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, embeddingsPath, tt.answer)
			c := newTestConnector(t, api, tt.cfg)

			require.Equal(t, tt.cfg.Dimensions, c.Dimensions())

			got, err := c.Embed(t.Context(), texts)

			body := requestBody(t, api, 0)

			var input []string
			require.NoError(t, json.Unmarshal(body["input"], &input))
			require.Equal(t, texts, input)
			require.JSONEq(t, `"`+tt.cfg.Model+`"`, string(body["model"]))
			require.JSONEq(t, `"float"`, string(body["encoding_format"]))
			require.Equal(t, "Bearer "+testAPIKey, api.Requests()[0].Header.Get("Authorization"))

			if tt.wantDimensions == 0 {
				require.NotContains(t, body, "dimensions")
			} else {
				require.JSONEq(t, `3`, string(body["dimensions"]))
			}

			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, tt.wantRetryAfter)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, [][]float32{{1, 0.75, 0.0625}, {0.5, 0.25, -0.125}}, got.Vectors)
			require.Equal(t, int64(12), got.Usage.InputTokens)
			require.Equal(t, tt.answer.headers["X-Request-Id"], got.RequestID)
		})
	}
}

func TestEmbedOnAGenerationConnector(t *testing.T) {
	t.Parallel()

	api := serve(t, embeddingsPath)
	c := newTestConnector(t, api, responsesConfig("gpt-6-sol"))

	require.Zero(t, c.Dimensions())

	_, err := c.Embed(t.Context(), []string{"a"})
	requireKind(t, err, model.ErrInvalid, 0)
	require.ErrorIs(t, err, errNotEmbedding)
}

func TestGenerateOnAnEmbeddingConnector(t *testing.T) {
	t.Parallel()

	api := serve(t, embeddingsPath)
	c := newTestConnector(t, api, embedConfig("text-embedding-3-small", 3))

	_, err := c.Generate(t.Context(), userText("hello"))
	requireKind(t, err, model.ErrInvalid, 0)
	require.ErrorIs(t, err, errEmbeddingOnly)
}

func TestEmbeddingConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// wantErr is a text the error holds; empty means a valid config.
		wantErr       string
		wantEmbedding bool
		wantDims      int
	}{
		{name: "known model with fewer dimensions", cfg: embedConfig("text-embedding-3-large", 1024), wantEmbedding: true, wantDims: 1024},
		{name: "known model with its native dimensions", cfg: embedConfig("text-embedding-3-small", 1536), wantEmbedding: true, wantDims: 1536},
		{name: "ada returns only its own length", cfg: embedConfig("text-embedding-ada-002", 1536), wantEmbedding: true, wantDims: 1536},
		{
			name:          "local server model with declared dimensions",
			cfg:           Config{Settings: model.Settings{Model: "nomic-embed-text"}, BaseURL: ollamaURL, Dimensions: 768},
			wantEmbedding: true, wantDims: 768,
		},
		{name: "known model without dimensions", cfg: embedConfig("text-embedding-3-small", 0), wantEmbedding: true, wantErr: "dimensions: required"},
		{name: "more dimensions than the model has", cfg: embedConfig("text-embedding-3-small", 2000), wantEmbedding: true, wantDims: 2000, wantErr: "dimensions: text-embedding-3-small returns at most 1536"},
		{name: "ada with other dimensions", cfg: embedConfig("text-embedding-ada-002", 512), wantEmbedding: true, wantDims: 512, wantErr: "dimensions: text-embedding-ada-002 always returns 1536"},
		{name: "model OpenAI does not offer", cfg: embedConfig("nomic-embed-text", 768), wantEmbedding: true, wantDims: 768, wantErr: "is not an embedding model OpenAI offers"},
		{name: "generation model", cfg: embedConfig("gpt-6-sol", 1536), wantEmbedding: true, wantDims: 1536, wantErr: "is a generation model"},
		{
			name: "key over plain http to another host",
			cfg: Config{
				Settings: model.Settings{Model: "nomic-embed-text"}, APIKeySecret: keyRef,
				BaseURL: "http://embeddings.internal:8080/v1", Dimensions: 768,
			},
			wantEmbedding: true, wantDims: 768, wantErr: "must use https",
		},
		{name: "no key without baseURL", cfg: Config{Settings: model.Settings{Model: "text-embedding-3-small"}, Dimensions: 3}, wantEmbedding: true, wantDims: 3, wantErr: "apiKeySecret: required without baseURL"},
		{
			name:          "generation settings",
			cfg:           Config{Settings: model.Settings{Model: "text-embedding-3-small", MaxTokens: 10, Effort: model.EffortLow}, APIKeySecret: keyRef, API: APIResponses, Dimensions: 3},
			wantEmbedding: true, wantDims: 3, wantErr: "maxTokens: not used for embeddings",
		},
		{name: "generation model without dimensions", cfg: responsesConfig("gpt-6-sol")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dims, ok := tt.cfg.Embedding()
			require.Equal(t, tt.wantEmbedding, ok)
			require.Equal(t, tt.wantDims, dims)

			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

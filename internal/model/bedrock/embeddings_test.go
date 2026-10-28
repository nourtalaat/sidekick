package bedrock

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
)

// Embedding model IDs of the tests. The Cohere one is an inference profile.
const (
	titanV2   = "amazon.titan-embed-text-v2:0"
	cohereGeo = "us.cohere.embed-v4:0"
)

// opInvoke is the InvokeModel operation, as the last segment of its path.
const opInvoke = "invoke"

// testDimensions is the length of the vectors in the recorded answers.
const testDimensions = 3

// embedTexts are the texts the tests embed.
var embedTexts = []string{"first alert", "second alert"}

// wantVectors are the vectors of the recorded answers for embedTexts.
var wantVectors = [][]float32{{0.5, 0.25, -0.125}, {1, 0.75, 0.0625}}

// titanAnswer is the recorded answer to the request for one text.
type titanAnswer struct {
	status  int
	file    string
	headers map[string]string
}

// serveTitan starts a fake Bedrock runtime that answers each Titan request
// by the text it holds, since the requests of one batch run at the same
// time and arrive in any order.
func serveTitan(t *testing.T, answers map[string]titanAnswer) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	api.HandleFunc(http.MethodPost, "/model/"+titanV2+"/"+opInvoke, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the request: %v", err)

			return
		}

		var req titanRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode the request: %v", err)

			return
		}

		a, ok := answers[req.InputText]
		if !ok {
			t.Errorf("unexpected text %q", req.InputText)
			w.WriteHeader(http.StatusNotImplemented)

			return
		}

		data, err := os.ReadFile(filepath.Join("fixture", a.file)) //nolint:gosec // A fixture path chosen by the test.
		if err != nil {
			t.Errorf("read the fixture: %v", err)

			return
		}

		w.Header().Set(headerContentType, contentTypeJSON)

		for k, v := range a.headers {
			w.Header().Set(k, v)
		}

		w.WriteHeader(a.status)

		if _, err := w.Write(data); err != nil {
			t.Errorf("write the answer: %v", err)
		}
	})

	api.ExpectAllCalled()

	return api
}

// embedEdit sets the dimensions of the recorded answers on a config.
func embedEdit(c *Config) {
	c.Dimensions = testDimensions
}

func TestEmbedTitan(t *testing.T) {
	t.Parallel()

	ok := func(file string) titanAnswer {
		return titanAnswer{status: http.StatusOK, file: file, headers: map[string]string{headerRequestID: "req-titan"}}
	}

	tests := []struct {
		name     string
		answers  map[string]titanAnswer
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
		wantErr        error
	}{
		{
			name: "one request for each text",
			answers: map[string]titanAnswer{
				"first alert":  ok("embed_titan_a.json"),
				"second alert": ok("embed_titan_b.json"),
			},
		},
		{
			name: "a vector of the wrong length",
			answers: map[string]titanAnswer{
				"first alert":  ok("embed_titan_a.json"),
				"second alert": ok("embed_titan_short.json"),
			},
			wantKind: model.ErrInvalid,
			wantErr:  errWrongVectorLength,
		},
		{
			name: "throttled with retry-after",
			answers: map[string]titanAnswer{
				"first alert": ok("embed_titan_a.json"),
				"second alert": {
					status: http.StatusTooManyRequests, file: "throttling.json",
					headers: map[string]string{headerErrorType: "ThrottlingException", model.RetryAfterHeader: "5"},
				},
			},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 5 * time.Second,
		},
		{
			name: "invalid request",
			answers: map[string]titanAnswer{
				"first alert":  {status: http.StatusBadRequest, file: "validation.json", headers: map[string]string{headerErrorType: "ValidationException"}},
				"second alert": ok("embed_titan_b.json"),
			},
			wantKind: model.ErrInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serveTitan(t, tt.answers)
			c := newTestConnector(t, api, titanV2, embedEdit)

			require.Equal(t, testDimensions, c.Dimensions())

			got, err := c.Embed(t.Context(), embedTexts)

			sent := api.Requests()
			require.Len(t, sent, len(embedTexts))

			for _, r := range sent {
				var req map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(r.Body, &req))
				require.JSONEq(t, `3`, string(req["dimensions"]))
				require.JSONEq(t, `true`, string(req["normalize"]))
				require.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+testAccessKey+"/")
			}

			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)
				require.Equal(t, tt.wantRetryAfter, merr.RetryAfter)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, wantVectors, got.Vectors)
			require.Equal(t, int64(13), got.Usage.InputTokens)
			require.Equal(t, "req-titan", got.RequestID)
		})
	}
}

func TestEmbedCohere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response recorded
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
		wantErr        error
		wantTokens     int64
	}{
		{
			name: "embeddings as a list, with the token count header",
			response: recorded{op: opInvoke, status: http.StatusOK, file: "embed_cohere_list.json", headers: map[string]string{
				headerRequestID: "req-cohere", "X-Amzn-Bedrock-Input-Token-Count": "9",
			}},
			wantTokens: 9,
		},
		{
			name:     "embeddings by type, without a token count",
			response: recorded{op: opInvoke, status: http.StatusOK, file: "embed_cohere_by_type.json", headers: map[string]string{headerRequestID: "req-cohere"}},
		},
		{
			name:     "an answer without float embeddings",
			response: recorded{op: opInvoke, status: http.StatusOK, file: "embed_cohere_no_float.json"},
			wantKind: model.ErrInvalid,
			wantErr:  errBadAnswer,
		},
		{
			name:     "fewer vectors than texts",
			response: recorded{op: opInvoke, status: http.StatusOK, file: "embed_cohere_missing.json"},
			wantKind: model.ErrInvalid,
			wantErr:  errBadAnswer,
		},
		{
			name:     "vectors of the wrong length",
			response: recorded{op: opInvoke, status: http.StatusOK, file: "embed_cohere_wrong_length.json"},
			wantKind: model.ErrInvalid,
			wantErr:  errWrongVectorLength,
		},
		{
			name: "throttled with retry-after",
			response: recorded{op: opInvoke, status: http.StatusTooManyRequests, file: "throttling.json", headers: map[string]string{
				headerErrorType: "ThrottlingException", model.RetryAfterHeader: "7",
			}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 7 * time.Second,
		},
		{
			name:     "access denied",
			response: recorded{op: opInvoke, status: http.StatusForbidden, file: "access_denied.json", headers: map[string]string{headerErrorType: "AccessDeniedException"}},
			wantKind: model.ErrAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, cohereGeo, tt.response)
			c := newTestConnector(t, api, cohereGeo, embedEdit)

			got, err := c.Embed(t.Context(), embedTexts)

			body := requestBody(t, api, 0)

			var texts []string
			require.NoError(t, json.Unmarshal(body["texts"], &texts))
			require.Equal(t, embedTexts, texts)
			require.JSONEq(t, `"search_document"`, string(body["input_type"]))
			require.JSONEq(t, `["float"]`, string(body["embedding_types"]))
			require.JSONEq(t, `3`, string(body["output_dimension"]))
			require.JSONEq(t, `"RIGHT"`, string(body["truncate"]))

			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)
				require.Equal(t, tt.wantRetryAfter, merr.RetryAfter)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, wantVectors, got.Vectors)
			require.Equal(t, tt.wantTokens, got.Usage.InputTokens)
			require.Equal(t, "req-cohere", got.RequestID)
		})
	}
}

func TestEmbedOnAGenerationConnector(t *testing.T) {
	t.Parallel()

	api := serve(t, opus55Geo)
	c := newTestConnector(t, api, opus55Geo, nil)

	require.Zero(t, c.Dimensions())

	_, err := c.Embed(t.Context(), embedTexts)

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrInvalid, merr.Kind)
	require.ErrorIs(t, err, errNotEmbedding)
}

func TestGenerateOnAnEmbeddingConnector(t *testing.T) {
	t.Parallel()

	api := serve(t, cohereGeo)
	c := newTestConnector(t, api, cohereGeo, embedEdit)

	_, err := c.Generate(t.Context(), userText("hello"))

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrInvalid, merr.Kind)
	require.ErrorIs(t, err, errEmbeddingOnly)
}

func TestEmbeddingConfig(t *testing.T) {
	t.Parallel()

	embed := func(modelID string, dimensions int) Config {
		return Config{Settings: model.Settings{Model: modelID}, Region: testRegion, Dimensions: dimensions}
	}

	tests := []struct {
		name string
		cfg  Config
		// wantErr is a text the error holds; empty means a valid config.
		wantErr       string
		wantEmbedding bool
		wantDims      int
	}{
		{name: "Titan with a length it returns", cfg: embed(titanV2, 1024), wantEmbedding: true, wantDims: 1024},
		{name: "Cohere through a profile", cfg: embed(cohereGeo, 1536), wantEmbedding: true, wantDims: 1536},
		{name: "Titan without dimensions", cfg: embed(titanV2, 0), wantEmbedding: true, wantErr: "dimensions: required"},
		{name: "Titan with a length it does not return", cfg: embed(titanV2, 1536), wantEmbedding: true, wantDims: 1536, wantErr: "dimensions: 1536 is not a length"},
		{name: "Cohere with a length it does not return", cfg: embed(cohereGeo, 768), wantEmbedding: true, wantDims: 768, wantErr: "dimensions: 768 is not a length"},
		{name: "a model Bedrock embedding support does not know", cfg: embed("amazon.titan-embed-text-v1", 1536), wantEmbedding: true, wantDims: 1536, wantErr: "is not an embedding model the bedrock provider knows"},
		{name: "a generation model with dimensions", cfg: embed(opus55Geo, 1024), wantEmbedding: true, wantDims: 1024, wantErr: "is not an embedding model"},
		{
			name:          "no region",
			cfg:           Config{Settings: model.Settings{Model: titanV2}, Dimensions: 256},
			wantEmbedding: true, wantDims: 256, wantErr: "region:",
		},
		{
			name:          "generation settings",
			cfg:           Config{Settings: model.Settings{Model: titanV2, MaxTokens: 5, Effort: model.EffortLow}, Region: testRegion, Dimensions: 256},
			wantEmbedding: true, wantDims: 256, wantErr: "maxTokens: not used for embeddings",
		},
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

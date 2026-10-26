package bedrock

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Reasoning in fixture/reasoning.jsonl, after its deltas are joined and
// its JSON escapes decoded. The redacted data is the base64 of the bytes
// the API sent.
const (
	recordedThinking  = "The error rate rose at 14:02. \"Pool exhausted\" appears in the logs.\n\tNext: check the déploiement list, <deploys> & ✓ metrics."
	recordedSignature = "EqQBCkgIARABGAIiQL+/7wSig9Fh2v1pXzQe+3k=="
	recordedRedacted  = "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpP"
	// omittedSignature is the signature of the reasoning block with no
	// text in fixture/reasoning_omitted.jsonl.
	omittedSignature = "ErUBCkYIBRgCIkB0bWl0dGVkLXRoaW5raW5nLXNpZ25hdHVyZQ=="
)

// Model IDs of the tests.
const (
	opus55Geo    = "us.anthropic.claude-opus-5-5"
	opus5Global  = "global.anthropic.claude-opus-5"
	opus48       = "anthropic.claude-opus-4-8"
	sonnet46Geo  = "eu.anthropic.claude-sonnet-4-6"
	novaDeclared = "amazon.nova-pro-v1:0"
)

// smallOutput is an output limit low enough for a plain Converse call.
const smallOutput = 8_000

func TestGenerate(t *testing.T) {
	t.Parallel()

	redacted := mustBase64(t, recordedRedacted)

	tests := []struct {
		name     string
		model    string
		edit     func(*Config)
		response recorded
		want     model.Response
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
	}{
		{
			name:     "streamed text turn",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "text.jsonl", headers: map[string]string{headerRequestID: "req-text"}},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The checkout service ran out of database connections."}}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 412, OutputTokens: 14},
				Model:      opus55Geo,
				RequestID:  "req-text",
			},
		},
		{
			name:     "streamed tool use",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "tool_use.jsonl"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Text{Text: "I will search the logs."},
					model.ToolCall{ID: "tooluse_01Search", Name: "search_logs", Input: json.RawMessage(`{"service": "checkout", "limit": 20}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 900, OutputTokens: 61, CacheWriteTokens: 2048},
				Model:      opus55Geo,
			},
		},
		{
			name:     "streamed reasoning with a signature and redacted reasoning",
			model:    opus5Global,
			edit:     func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingAdaptive} },
			response: recorded{op: opStream, status: http.StatusOK, file: "reasoning.jsonl"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, Text: recordedThinking, Signature: recordedSignature},
					model.Reasoning{Provider: Provider, Redacted: redacted},
					model.ToolCall{ID: "tooluse_01Deploys", Name: "list_deploys", Input: json.RawMessage(`{"since": "13:30"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 120, OutputTokens: 210, CacheReadTokens: 5000, CacheWriteTokens: 300},
				Model:      opus5Global,
			},
		},
		{
			name:     "streamed reasoning with a signature and no text",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "reasoning_omitted.jsonl"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, Signature: omittedSignature},
					model.ToolCall{ID: "tooluse_01Pods", Name: "k8s__list_pods", Input: json.RawMessage(`{"namespace": "shop"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 95},
				Model:      opus55Geo,
			},
		},
		{
			name:     "converse tool use",
			model:    opus55Geo,
			edit:     func(c *Config) { c.MaxTokens = smallOutput },
			response: recorded{op: opConverse, status: http.StatusOK, file: "converse_tool_use.json", headers: map[string]string{headerRequestID: "req-tool"}},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Text{Text: "I will search the logs."},
					model.ToolCall{ID: "tooluse_02Search", Name: "search_logs", Input: json.RawMessage(`{"limit":20,"service":"checkout"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 900, OutputTokens: 61, CacheReadTokens: 6000},
				Model:      opus55Geo,
				RequestID:  "req-tool",
			},
		},
		{
			name:     "converse reasoning",
			model:    opus55Geo,
			edit:     func(c *Config) { c.MaxTokens = smallOutput },
			response: recorded{op: opConverse, status: http.StatusOK, file: "converse_reasoning.json"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, Text: "Deploy 812 went out at 13:58, four minutes before the errors.", Signature: "EpYBCkYIBRgCIkDjb252ZXJzZS1zaWduYXR1cmU="},
					model.Reasoning{Provider: Provider, Redacted: mustBase64(t, "RW13S0FoZ0JFZ3kzdmEzcHppeA==")},
					model.Text{Text: "Deploy 812 is the likely cause."},
				}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 1500, OutputTokens: 340},
				Model:      opus55Geo,
			},
		},
		{
			name:     "max tokens",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "max_tokens.jsonl"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The first cause is"}}},
				StopReason: model.StopMaxTokens,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 5},
				Model:      opus55Geo,
			},
		},
		{
			name:     "content filtered is a refusal",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "content_filtered.jsonl"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{}},
				StopReason: model.StopRefusal,
				Usage:      model.Usage{InputTokens: 80},
				Model:      opus55Geo,
			},
		},
		{
			name:  "throttled with retry-after",
			model: opus55Geo,
			response: recorded{op: opStream, status: http.StatusTooManyRequests, file: "throttling.json", headers: map[string]string{
				headerErrorType: "ThrottlingException", retryAfterHeader: "12",
			}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 12 * time.Second,
		},
		{
			name:     "throttled in the middle of the stream",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "throttling_stream.jsonl"},
			wantKind: model.ErrRateLimited,
		},
		{
			name:     "a model error in the middle of the stream",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "model_stream_error.jsonl"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "validation exception",
			model:    opus55Geo,
			edit:     func(c *Config) { c.MaxTokens = smallOutput },
			response: recorded{op: opConverse, status: http.StatusBadRequest, file: "validation.json", headers: map[string]string{headerErrorType: "ValidationException"}},
			wantKind: model.ErrInvalid,
		},
		{
			name:     "prompt too long",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusBadRequest, file: "context_length.json", headers: map[string]string{headerErrorType: "ValidationException"}},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "access denied",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusForbidden, file: "access_denied.json", headers: map[string]string{headerErrorType: "AccessDeniedException"}},
			wantKind: model.ErrAuth,
		},
		{
			name:     "service unavailable",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusServiceUnavailable, file: "service_unavailable.json", headers: map[string]string{headerErrorType: "ServiceUnavailableException"}},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "model timeout",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusRequestTimeout, file: "model_timeout.json", headers: map[string]string{headerErrorType: "ModelTimeoutException"}},
			wantKind: model.ErrTimeout,
		},
		{
			name:     "an error code the connector does not know falls back to the status",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusBadGateway, file: "service_unavailable.json", headers: map[string]string{headerErrorType: "SomeNewException"}},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "stream that ends early",
			model:    opus55Geo,
			response: recorded{op: opStream, status: http.StatusOK, file: "truncated.jsonl"},
			wantKind: model.ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.model, tt.response)
			c := newTestConnector(t, api, tt.model, tt.edit)

			resp, err := c.Generate(t.Context(), userText("Why did checkout fail?"))
			sent := api.Requests()
			require.Len(t, sent, 1)
			require.True(t, strings.HasPrefix(sent[0].Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="+testAccessKey+"/"),
				"the request is signed with the AWS credentials")

			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)
				require.Equal(t, tt.wantRetryAfter, merr.RetryAfter)
				require.ErrorContains(t, err, "connector claude")

				return
			}

			require.NoError(t, err)
			require.Positive(t, resp.Latency)

			resp.Latency = 0
			require.Equal(t, tt.want, resp)
		})
	}
}

package google

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Thought signatures in the recorded responses, as the API sends them.
const (
	textSignature    = "Q2lRQjBlMnhhbWFNWDZyNFlFbVRPNmw="
	callSignature    = "RXJnQ0Nyc0NBYjRDMnhaNXB4OFRrRXA="
	thoughtSignature = "Q3BJQkFkSDdTTF9wT2VMNHZpV3c="
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		path     string
		response recorded
		want     model.Response
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
	}{
		{
			name:     "text over several chunks with a signature at the end",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "text.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: textSignature},
					model.Text{Text: "The checkout service ran out of database connections."},
				}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 266, OutputTokens: 138, CacheReadTokens: 1024, ReasoningTokens: 126},
				Model:      flash,
				RequestID:  "sYJbaZ-tAsCN1dkPu4TkgQk",
			},
		},
		{
			name:     "parallel tool calls on vertex",
			cfg:      vertexConfig(flash),
			path:     vertexPath,
			response: recorded{status: http.StatusOK, file: "tool_calls.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, ID: reasoningNext, Signature: callSignature},
					model.ToolCall{ID: "w2f9x1ka", Name: "list_deploys", Input: json.RawMessage(`{"service":"checkout","since":"13:30"}`)},
					model.ToolCall{ID: "q8z3m0pd", Name: "search_logs", Input: json.RawMessage(`{"limit":20,"query":"level:error"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 2100, OutputTokens: 310, ReasoningTokens: 262},
				Model:      flash,
				RequestID:  "aXJbaY7RL6u21dkP8NGd6Q0",
			},
		},
		{
			name:     "thought summary before the answer",
			cfg:      geminiConfig("gemini-2.5-pro"),
			path:     "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			response: recorded{status: http.StatusOK, file: "thought.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, ID: reasoningThought, Text: "**Checking the deploys**\n\nThe errors start at 13:42, right after a deploy.", Signature: thoughtSignature},
					model.Text{Text: "A deploy at 13:40 caused it."},
				}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 640, OutputTokens: 461, ReasoningTokens: 452},
				Model:      "gemini-2.5-pro",
				RequestID:  "eXJbaZbGBey21dkPgbbm0Qs",
			},
		},
		{
			name:     "output limit",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "max_tokens.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The first cause is"}}},
				StopReason: model.StopMaxTokens,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 40, ReasoningTokens: 35},
				Model:      flash,
				RequestID:  "mXJbaYv3AuG21dkP7Mru2A4",
			},
		},
		{
			name:     "answer stopped by a safety filter",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "safety.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{}},
				StopReason: model.StopRefusal,
				Usage:      model.Usage{InputTokens: 410},
				Model:      flash,
				RequestID:  "nXJbaZPGDK621dkP0Kvh2Q0",
			},
		},
		{
			name:     "prompt blocked",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "blocked.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant},
				StopReason: model.StopRefusal,
				Usage:      model.Usage{InputTokens: 380},
				Model:      flash,
				RequestID:  "oXJbaYq3H9G21dkP_cGM6Aw",
			},
		},
		{
			name:           "rate limited with a retry delay in the body",
			cfg:            geminiConfig(flash),
			path:           geminiPath,
			response:       recorded{status: http.StatusTooManyRequests, file: "rate_limit.json"},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 37 * time.Second,
		},
		{
			name:     "overloaded",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusServiceUnavailable, file: "overloaded.json"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "bad API key",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusBadRequest, file: "bad_key.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "free tier not offered in the region",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusBadRequest, file: "location.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "missing permission on vertex",
			cfg:      vertexConfig(flash),
			path:     vertexPath,
			response: recorded{status: http.StatusForbidden, file: "permission_denied.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "context length",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusBadRequest, file: "context_length.json"},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "invalid request",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusBadRequest, file: "invalid.json"},
			wantKind: model.ErrInvalid,
		},
		{
			name:     "error part way through the stream",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "stream_error.sse"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "stream that ends before the answer finished",
			cfg:      geminiConfig(flash),
			path:     geminiPath,
			response: recorded{status: http.StatusOK, file: "truncated.sse"},
			wantKind: model.ErrOverloaded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.path, tt.response)
			c := newTestConnector(t, api, tt.cfg)

			got, err := c.Generate(t.Context(), userText("Why is checkout failing?"))
			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, tt.wantRetryAfter)

				return
			}

			require.NoError(t, err)
			require.Positive(t, got.Latency)

			got.Latency = 0
			require.Equal(t, tt.want, got)
		})
	}
}

// TestGenerateMakesUpMissingCallIDs checks that a tool call the API sent
// without an ID gets one, and that the made-up ID is not sent back.
func TestGenerateMakesUpMissingCallIDs(t *testing.T) {
	t.Parallel()

	const path = "/v1beta/models/gemini-2.5-flash:streamGenerateContent"

	api := serve(t, path,
		recorded{status: http.StatusOK, file: "tool_call_no_id.sse"},
		recorded{status: http.StatusOK, file: "text.sse"},
	)
	c := newTestConnector(t, api, geminiConfig("gemini-2.5-flash"))

	req := userText("Why is checkout failing?")

	first, err := c.Generate(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, first.Message.Parts, 1)

	call, ok := first.Message.Parts[0].(model.ToolCall)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(call.ID, generatedCallPrefix), call.ID)

	req.Messages = append(req.Messages, first.Message, model.Message{Role: model.User, Parts: []model.Part{
		model.ToolResult{CallID: call.ID, Content: "deploy 41 at 13:40"},
	}})

	_, err = c.Generate(t.Context(), req)
	require.NoError(t, err)

	contents := requestBody(t, api, 1)["contents"]
	require.JSONEq(t, `[
		{"role": "user", "parts": [{"text": "Why is checkout failing?"}]},
		{"role": "model", "parts": [{"functionCall": {"name": "list_deploys", "args": {"service": "checkout"}}}]},
		{"role": "user", "parts": [{"functionResponse": {"name": "list_deploys", "response": {"output": "deploy 41 at 13:40"}}}]}
	]`, toJSON(t, contents))
}

// TestReasoningIsSentBackUnchanged checks that the thought signature of
// the first turn reaches the API in the second, in the part it came in.
func TestReasoningIsSentBackUnchanged(t *testing.T) {
	t.Parallel()

	api := serve(t, vertexPath,
		recorded{status: http.StatusOK, file: "tool_calls.sse"},
		recorded{status: http.StatusOK, file: "text.sse"},
	)
	c := newTestConnector(t, api, vertexConfig(flash))

	req := userText("Why is checkout failing?")

	first, err := c.Generate(t.Context(), req)
	require.NoError(t, err)

	req.Messages = append(req.Messages, first.Message, model.Message{Role: model.User, Parts: []model.Part{
		model.ToolResult{CallID: "w2f9x1ka", Content: "deploy 41 at 13:40"},
		model.ToolResult{CallID: "q8z3m0pd", Content: "connection refused", IsError: true},
	}})

	_, err = c.Generate(t.Context(), req)
	require.NoError(t, err)

	contents := requestBody(t, api, 1)["contents"]
	require.JSONEq(t, `[
		{"role": "user", "parts": [{"text": "Why is checkout failing?"}]},
		{"role": "model", "parts": [
			{"functionCall": {"id": "w2f9x1ka", "name": "list_deploys", "args": {"service": "checkout", "since": "13:30"}}, "thoughtSignature": "`+callSignature+`"},
			{"functionCall": {"id": "q8z3m0pd", "name": "search_logs", "args": {"query": "level:error", "limit": 20}}}
		]},
		{"role": "user", "parts": [
			{"functionResponse": {"id": "w2f9x1ka", "name": "list_deploys", "response": {"output": "deploy 41 at 13:40"}}},
			{"functionResponse": {"id": "q8z3m0pd", "name": "search_logs", "response": {"error": "connection refused"}}}
		]}
	]`, toJSON(t, contents))
}

func TestGenerateRequestBody(t *testing.T) {
	t.Parallel()

	tools := []model.ToolSpec{
		{Name: "search_logs", Description: "Search the logs.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)},
		{Name: "submit_verdict", Description: "Submit the verdict."},
	}

	tests := []struct {
		name string
		cfg  Config
		path string
		req  model.Request
		// want holds the fields of the generationConfig, and of the top
		// level of the body, that the case checks.
		want map[string]string
		// absent are top-level body fields that must not be sent.
		absent []string
	}{
		{
			name: "effort becomes the thinking level",
			cfg:  geminiConfig(flash),
			path: geminiPath,
			req:  model.Request{Effort: model.EffortLow, MaxTokens: 2000, System: []model.Text{{Text: "You investigate alerts."}}},
			want: map[string]string{
				"generationConfig":  `{"maxOutputTokens": 2000, "thinkingConfig": {"thinkingLevel": "LOW"}}`,
				"systemInstruction": `{"role": "user", "parts": [{"text": "You investigate alerts."}]}`,
			},
			absent: []string{"tools", "toolConfig", "labels"},
		},
		{
			name: "settings from config",
			cfg: Config{
				Settings:     model.Settings{Model: flash, Effort: model.EffortHigh, MaxTokens: 9000, Temperature: ptr(0.5)},
				APIKeySecret: keyRef,
			},
			path: geminiPath,
			want: map[string]string{
				"generationConfig": `{"maxOutputTokens": 9000, "temperature": 0.5, "thinkingConfig": {"thinkingLevel": "HIGH"}}`,
			},
		},
		{
			name: "adaptive thinking on a budget model is a dynamic budget",
			cfg:  Config{Settings: model.Settings{Model: "gemini-2.5-flash-lite", Thinking: model.Thinking{Mode: model.ThinkingAdaptive}}, APIKeySecret: keyRef},
			path: "/v1beta/models/gemini-2.5-flash-lite:streamGenerateContent",
			want: map[string]string{"generationConfig": `{"thinkingConfig": {"thinkingBudget": -1}}`},
		},
		{
			name: "fixed budget",
			cfg:  geminiConfig("gemini-2.5-pro"),
			path: "/v1beta/models/gemini-2.5-pro:streamGenerateContent",
			req:  model.Request{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096}},
			want: map[string]string{"generationConfig": `{"thinkingConfig": {"thinkingBudget": 4096}}`},
		},
		{
			name: "thinking off",
			cfg:  geminiConfig("gemini-2.5-flash"),
			path: "/v1beta/models/gemini-2.5-flash:streamGenerateContent",
			req:  model.Request{Thinking: model.Thinking{Mode: model.ThinkingOff}},
			want: map[string]string{"generationConfig": `{"thinkingConfig": {"thinkingBudget": 0}}`},
		},
		{
			name: "named tool forced",
			cfg:  geminiConfig(flash),
			path: geminiPath,
			req:  model.Request{Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"}},
			want: map[string]string{
				"tools": `[{"functionDeclarations": [
					{"name": "search_logs", "description": "Search the logs.", "parametersJsonSchema": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}},
					{"name": "submit_verdict", "description": "Submit the verdict."}
				]}]`,
				"toolConfig": `{"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": ["submit_verdict"]}}`,
			},
		},
		{
			name: "any tool",
			cfg:  geminiConfig(flash),
			path: geminiPath,
			req:  model.Request{Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
			want: map[string]string{"toolConfig": `{"functionCallingConfig": {"mode": "ANY"}}`},
		},
		{
			name: "no tool",
			cfg:  geminiConfig(flash),
			path: geminiPath,
			req:  model.Request{Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			want: map[string]string{"toolConfig": `{"functionCallingConfig": {"mode": "NONE"}}`},
		},
		{
			name:   "tools without a tool choice",
			cfg:    geminiConfig(flash),
			path:   geminiPath,
			req:    model.Request{Tools: tools},
			absent: []string{"toolConfig"},
		},
		{
			name: "labels on vertex, only the valid ones",
			cfg:  vertexConfig(flash),
			path: vertexPath,
			req:  model.Request{Metadata: map[string]string{"investigation": "inv-0192", "Bad Key": "x", "route": "Has Spaces"}},
			want: map[string]string{"labels": `{"investigation": "inv-0192"}`},
		},
		{
			name:   "no labels on the gemini API",
			cfg:    geminiConfig(flash),
			path:   geminiPath,
			req:    model.Request{Metadata: map[string]string{"investigation": "inv-0192"}},
			absent: []string{"labels"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.path, recorded{status: http.StatusOK, file: "text.sse"})
			c := newTestConnector(t, api, tt.cfg)

			req := tt.req
			req.Messages = userText("hi").Messages

			_, err := c.Generate(t.Context(), req)
			require.NoError(t, err)

			body := requestBody(t, api, 0)
			for field, want := range tt.want {
				require.JSONEq(t, want, toJSON(t, body[field]), field)
			}

			for _, field := range tt.absent {
				require.NotContains(t, body, field)
			}
		})
	}
}

// TestGenerateAuth checks the credentials each backend sends, read again
// on every call.
func TestGenerateAuth(t *testing.T) {
	t.Parallel()

	t.Run("gemini reads the key on every call", func(t *testing.T) {
		t.Parallel()

		api := serve(t, geminiPath,
			recorded{status: http.StatusOK, file: "text.sse"},
			recorded{status: http.StatusOK, file: "text.sse"},
		)

		ctrl := gomock.NewController(t)
		s := registry.NewMockSecrets(ctrl)
		gomock.InOrder(
			s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1\n"), nil).Times(2),
			s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-2\n"), nil),
		)

		cfg := geminiConfig(flash)
		cfg.BaseURL = api.URL()

		c, err := New(t.Context(), "gemini", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
		require.NoError(t, err)

		for range 2 {
			_, err := c.Generate(t.Context(), userText("hi"))
			require.NoError(t, err)
		}

		sent := api.Requests()
		require.Equal(t, "key-1", sent[0].Header.Get(apiKeyHeader))
		require.Equal(t, "key-2", sent[1].Header.Get(apiKeyHeader))
		require.Empty(t, sent[0].Header.Get(authHeader))
	})

	t.Run("vertex sends a bearer token and the quota project", func(t *testing.T) {
		t.Parallel()

		api := serve(t, vertexPath, recorded{status: http.StatusOK, file: "text.sse"})
		c := newTestConnector(t, api, vertexConfig(flash))

		_, err := c.Generate(t.Context(), userText("hi"))
		require.NoError(t, err)

		sent := api.Requests()
		require.Equal(t, "Bearer "+testAccessToken, sent[0].Header.Get(authHeader))
		require.Equal(t, testQuotaProject, sent[0].Header.Get(userProjectHeader))
		require.Empty(t, sent[0].Header.Get(apiKeyHeader))
	})
}

// TestGenerateCredentialErrors checks that credentials that cannot be read
// at call time are an auth error and send nothing.
func TestGenerateCredentialErrors(t *testing.T) {
	t.Parallel()

	t.Run("key", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)

		ctrl := gomock.NewController(t)
		s := registry.NewMockSecrets(ctrl)
		gomock.InOrder(
			s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1"), nil),
			s.EXPECT().Resolve(gomock.Any(), keyRef).Return(nil, errors.New("gone")),
		)

		cfg := geminiConfig(flash)
		cfg.BaseURL = api.URL()

		c, err := New(t.Context(), "gemini", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
		require.NoError(t, err)

		_, err = c.Generate(t.Context(), userText("hi"))
		requireKind(t, err, model.ErrAuth, 0)
		require.Empty(t, api.Requests())
	})

	t.Run("token", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)

		cfg := vertexConfig(flash)
		cfg.BaseURL = api.URL()

		find := func(context.Context, *http.Client) (tokenSource, string, error) {
			return fixedToken{err: errors.New("metadata server unreachable")}, "", nil
		}

		c, err := build(t.Context(), "gemini", cfg, registry.Deps{HTTP: api.Client()}, find)
		require.NoError(t, err)

		_, err = c.Generate(t.Context(), userText("hi"))
		requireKind(t, err, model.ErrAuth, 0)
		require.Empty(t, api.Requests())
	})
}

// TestGenerateDoesNotRetry checks that the SDK sends one request for a
// failed call, so retries are left to model.Generate.
func TestGenerateDoesNotRetry(t *testing.T) {
	t.Parallel()

	api := serve(t, geminiPath, recorded{status: http.StatusServiceUnavailable, file: "overloaded.json"})
	c := newTestConnector(t, api, geminiConfig(flash))

	_, err := c.Generate(t.Context(), userText("hi"))
	requireKind(t, err, model.ErrOverloaded, 0)
	require.Len(t, api.Requests(), 1)
}

func TestGenerateRejectsBeforeSending(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		req  model.Request
	}{
		{name: "effort the model lacks", cfg: geminiConfig(flash), req: model.Request{Effort: model.EffortMinimal}},
		{name: "thinking off on a model that always thinks", cfg: geminiConfig(flash), req: model.Request{Thinking: model.Thinking{Mode: model.ThinkingOff}}},
		{name: "budget below the model's minimum", cfg: geminiConfig("gemini-2.5-pro"), req: model.Request{Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 64}}},
		{name: "forced tool without a name", cfg: geminiConfig(flash), req: model.Request{ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired}}},
		{
			name: "forced tool on a model that ignores it",
			cfg:  Config{Settings: model.Settings{Model: "tuned-flash"}, APIKeySecret: keyRef, Declared: &DeclaredCapabilities{ToolCalling: true}},
			req:  model.Request{ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
		},
		{
			name: "tool result for an unknown call",
			cfg:  geminiConfig(flash),
			req:  model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "nope", Content: "x"}}}}},
		},
		{
			name: "tool call from the user",
			cfg:  geminiConfig(flash),
			req:  model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.ToolCall{ID: "c1", Name: "x"}}}}},
		},
		{
			name: "tool schema that is not JSON",
			cfg:  geminiConfig(flash),
			req:  model.Request{Tools: []model.ToolSpec{{Name: "x", InputSchema: json.RawMessage(`{`)}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			c := newTestConnector(t, api, tt.cfg)

			_, err := c.Generate(t.Context(), tt.req)
			requireKind(t, err, model.ErrInvalid, 0)
			require.Empty(t, api.Requests())
		})
	}
}

func TestCheckKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		file     string
		wantKind model.ErrorKind
	}{
		{name: "valid key", status: http.StatusOK, file: "models.json"},
		{name: "rejected key", status: http.StatusBadRequest, file: "bad_key.json", wantKind: model.ErrAuth},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/v1beta/models", fakehttp.Response{
				Status:   tt.status,
				Headers:  map[string]string{"Content-Type": contentTypeJSON},
				BodyFile: "fixture/" + tt.file,
			})
			api.ExpectAllCalled()

			c := newTestConnector(t, api, geminiConfig(flash))

			err := c.CheckKey(t.Context())
			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, 0)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testAPIKey, api.Requests()[0].Header.Get(apiKeyHeader))
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	errNoADC := errors.New("could not find default credentials")

	tests := []struct {
		name string
		cfg  Config
		// noSecrets builds the connector without a secrets resolver.
		noSecrets bool
		// secretErr is what resolving the key returns.
		secretErr error
		// credErr is what finding the Vertex AI credentials returns.
		credErr error
		wantErr error
	}{
		{name: "gemini with a key", cfg: geminiConfig(flash)},
		{name: "vertex with credentials", cfg: vertexConfig(flash), noSecrets: true},
		{name: "key that cannot be read", cfg: geminiConfig(flash), secretErr: fs.ErrNotExist, wantErr: fs.ErrNotExist},
		{name: "key without secrets", cfg: geminiConfig(flash), noSecrets: true, wantErr: errNoSecrets},
		{name: "vertex without credentials", cfg: vertexConfig(flash), credErr: errNoADC, wantErr: errNoADC},
		{name: "unknown model", cfg: geminiConfig("gemini-9"), wantErr: errUnknownModel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			s := registry.NewMockSecrets(ctrl)

			if tt.cfg.backend() == BackendGemini && !tt.noSecrets && !errors.Is(tt.wantErr, errUnknownModel) {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey), tt.secretErr)
			}

			deps := registry.Deps{Secrets: s}
			if tt.noSecrets {
				deps.Secrets = nil
			}

			c, err := build(t.Context(), "gemini", tt.cfg, deps, fixedCredentials(tt.credErr))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.cfg.Model, c.ModelID())
		})
	}
}

// TestRegistered checks that the provider decodes and validates its section
// through the registry.
func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		wantErr string
	}{
		{name: "valid", section: "model: gemini-3.8-flash\nbackend: vertex\nproject: sidekick-test\n"},
		{name: "invalid", section: "model: gemini-3.8-flash\n", wantErr: "apiKeySecret: required with backend gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			_, err := registry.Models.Check(t.Context(), Provider, "gemini", node.Content[0])
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// toJSON encodes v, a part of a decoded request body, as JSON.
func toJSON(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	require.NoError(t, err)

	return string(data)
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T {
	return &v
}

package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Text of the thinking block in fixture/thinking.sse, after its deltas are
// joined and its JSON escapes decoded.
const (
	recordedThinking  = "The error rate rose at 14:02. \"Pool exhausted\" appears in the logs.\n\tNext: check the déploiement list, <deploys> & ✓ metrics."
	recordedSignature = "EqQBCkgIARABGAIiQL+/7wSig9Fh2v1pXzQe+3k=="
	recordedRedacted  = "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlxh0L5L8rLVyIwxtE3rAFBa8cr3qpP"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

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
			name:     "text turn",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "text.sse", headers: map[string]string{requestIDHeader: "req_011Text"}},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The checkout service ran out of database connections."}}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 412, OutputTokens: 14},
				Model:      "claude-opus-5-5",
				RequestID:  "req_011Text",
			},
		},
		{
			name:     "tool use",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "tool_use.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Text{Text: "I will search the logs."},
					model.ToolCall{ID: "toolu_01Search", Name: "search_logs", Input: json.RawMessage(`{"service": "checkout", "query": "level:error"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 900, OutputTokens: 61, CacheWriteTokens: 2048},
				Model:      "claude-opus-5-5",
			},
		},
		{
			name:  "thinking with a signature",
			model: "claude-haiku-4-5",
			edit: func(c *Config) {
				c.Thinking = model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}
				c.MaxTokens = 8000
			},
			response: recorded{status: http.StatusOK, file: "thinking.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, Text: recordedThinking, Signature: recordedSignature},
					model.Reasoning{Provider: Provider, Redacted: []byte(recordedRedacted)},
					model.ToolCall{ID: "toolu_01Deploys", Name: "list_deploys", Input: json.RawMessage(`{"since": "13:30"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 120, OutputTokens: 210, CacheReadTokens: 5000, CacheWriteTokens: 300, ReasoningTokens: 150},
				Model:      "claude-haiku-4-5",
			},
		},
		{
			name:     "refusal",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "refusal.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{}},
				StopReason: model.StopRefusal,
				Usage:      model.Usage{InputTokens: 80},
				Model:      "claude-opus-5-5",
			},
		},
		{
			name:     "max tokens",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "max_tokens.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The first cause is"}}},
				StopReason: model.StopMaxTokens,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 5},
				Model:      "claude-opus-5-5",
			},
		},
		{
			name:           "rate limited with retry-after",
			model:          "claude-opus-5-5",
			response:       recorded{status: http.StatusTooManyRequests, file: "rate_limit.json", headers: map[string]string{retryAfterHeader: "30"}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 30 * time.Second,
		},
		{
			name:     "overloaded",
			model:    "claude-opus-5-5",
			response: recorded{status: statusOverloaded, file: "overloaded.json"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "overloaded in the middle of the stream",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "overloaded_stream.sse"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "bad API key",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusUnauthorized, file: "auth.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "prompt too long",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusBadRequest, file: "context_length.json"},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "invalid request",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusBadRequest, file: "invalid.json"},
			wantKind: model.ErrInvalid,
		},
		{
			name:     "stream that ends early",
			model:    "claude-opus-5-5",
			response: recorded{status: http.StatusOK, file: "truncated.sse"},
			wantKind: model.ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.response)
			c := newTestConnector(t, api, tt.model, tt.edit)

			resp, err := c.Generate(t.Context(), userText("Why did checkout fail?"))
			sent := api.Requests()
			require.Len(t, sent, 1)
			require.Contains(t, sentKey(sent[0]), testAPIKey)

			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)
				require.Equal(t, tt.wantRetryAfter, merr.RetryAfter)

				return
			}

			require.NoError(t, err)
			require.Positive(t, resp.Latency)

			resp.Latency = 0
			require.Equal(t, tt.want, resp)
		})
	}
}

func TestGenerateRequestBody(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"LogQL"}},"required":["query"],"additionalProperties":false}`)

	tests := []struct {
		name  string
		model string
		edit  func(*Config)
		req   model.Request
		// want maps top-level keys of the request body to their JSON.
		want   map[string]string
		absent []string
	}{
		{
			name:  "defaults: streamed, default output limit, no thinking or effort",
			model: "claude-opus-5-5",
			req:   userText("hi"),
			want: map[string]string{
				"model":      `"claude-opus-5-5"`,
				"max_tokens": `64000`,
				"stream":     `true`,
				"messages":   `[{"role":"user","content":[{"type":"text","text":"hi"}]}]`,
			},
			absent: []string{"thinking", "output_config", "tool_choice", "tools", "system", "temperature"},
		},
		{
			name:  "config settings apply when the request leaves them out",
			model: "claude-opus-5-5",
			edit: func(c *Config) {
				c.MaxTokens = 16000
				c.Effort = model.EffortHigh
				c.Thinking = model.Thinking{Mode: model.ThinkingAdaptive}
			},
			req: userText("hi"),
			want: map[string]string{
				"max_tokens":    `16000`,
				"output_config": `{"effort":"high"}`,
				"thinking":      `{"type":"adaptive"}`,
			},
		},
		{
			name:  "request settings win over config",
			model: "claude-opus-5-5",
			edit:  func(c *Config) { c.MaxTokens = 16000; c.Effort = model.EffortHigh },
			req: model.Request{
				Messages:  userText("hi").Messages,
				MaxTokens: 1000,
				Effort:    model.EffortLow,
			},
			want: map[string]string{"max_tokens": `1000`, "output_config": `{"effort":"low"}`},
		},
		{
			name:  "output limit is capped at the model's maximum",
			model: "claude-haiku-4-5",
			req:   model.Request{Messages: userText("hi").Messages, MaxTokens: 100_000},
			want:  map[string]string{"max_tokens": `64000`},
		},
		{
			name:  "thinking off is sent as disabled where the model takes it",
			model: "claude-opus-5",
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingOff} },
			req:   userText("hi"),
			want:  map[string]string{"thinking": `{"type":"disabled"}`},
		},
		{
			name:   "thinking off is left out where off is the default",
			model:  "claude-opus-4-8",
			edit:   func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingOff} },
			req:    userText("hi"),
			absent: []string{"thinking"},
		},
		{
			name:  "a fixed thinking budget",
			model: "claude-sonnet-4-6",
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096} },
			req:   userText("hi"),
			want:  map[string]string{"thinking": `{"budget_tokens":4096,"type":"enabled"}`},
		},
		{
			name:  "prompt cache breakpoints on system text, tools and messages",
			model: "claude-opus-5-5",
			req: model.Request{
				System: []model.Text{{Text: "You investigate alerts.", Cache: true}, {Text: "Today is Monday."}},
				Tools:  []model.ToolSpec{{Name: "search_logs", Description: "Search logs.", InputSchema: schema, Cache: true}},
				Messages: []model.Message{{Role: model.User, Parts: []model.Part{
					model.Text{Text: "alert payload", Cache: true},
					model.Text{Text: "question"},
				}}},
			},
			want: map[string]string{
				"system": `[{"text":"You investigate alerts.","type":"text","cache_control":{"type":"ephemeral"}},{"text":"Today is Monday.","type":"text"}]`,
				"tools": `[{"input_schema":{"properties":{"query":{"type":"string","description":"LogQL"}},"required":["query"],"type":"object","additionalProperties":false},` +
					`"name":"search_logs","description":"Search logs.","cache_control":{"type":"ephemeral"}}]`,
				"messages": `[{"role":"user","content":[{"text":"alert payload","type":"text","cache_control":{"type":"ephemeral"}},{"text":"question","type":"text"}]}]`,
			},
		},
		{
			name:  "forced tool choice",
			model: "claude-opus-5",
			req:   model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"}},
			want:  map[string]string{"tool_choice": `{"name":"submit_verdict","type":"tool"}`},
		},
		{
			name:  "tool choice none",
			model: "claude-opus-5-5",
			req:   model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			want:  map[string]string{"tool_choice": `{"type":"none"}`},
		},
		{
			name:  "tool calls and results, and reasoning from another provider dropped",
			model: "claude-opus-5-5",
			req: model.Request{Messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.Text{Text: "why?"}}},
				{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: "openai", ID: "rs_1", Signature: "opaque"},
					model.ToolCall{ID: "toolu_1", Name: "search_logs", Input: json.RawMessage(`{"query": "x"}`)},
				}},
				{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "toolu_1", Content: "no data", IsError: true}}},
				{Role: model.Assistant, Parts: []model.Part{model.Reasoning{Provider: "openai", ID: "rs_2"}}},
			}},
			want: map[string]string{
				"messages": `[{"role":"user","content":[{"text":"why?","type":"text"}]},` +
					`{"role":"assistant","content":[{"id":"toolu_1","input":{"query": "x"},"name":"search_logs","type":"tool_use"}]},` +
					`{"role":"user","content":[{"tool_use_id":"toolu_1","is_error":true,"content":[{"text":"no data","type":"text"}],"type":"tool_result"}]}]`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, recorded{status: http.StatusOK, file: "text.sse"})
			c := newTestConnector(t, api, tt.model, tt.edit)

			_, err := c.Generate(t.Context(), tt.req)
			require.NoError(t, err)

			body := requestBody(t, api, 0)
			for key, want := range tt.want {
				require.Contains(t, body, key)
				require.JSONEq(t, want, string(body[key]), "key %s", key)
			}

			for _, key := range tt.absent {
				require.NotContains(t, body, key)
			}
		})
	}
}

func TestGenerateRejectsBeforeSending(t *testing.T) {
	t.Parallel()

	temperature := 0.2
	breakpoints := make([]model.Text, maxCacheBreakpoints+1)

	for i := range breakpoints {
		breakpoints[i] = model.Text{Text: "part", Cache: true}
	}

	tests := []struct {
		name    string
		model   string
		req     model.Request
		wantErr error
		wantMsg string
	}{
		{
			name:    "temperature",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: userText("hi").Messages, Temperature: &temperature},
			wantErr: errTemperature,
		},
		{
			name:    "forced tool choice on a model that refuses it",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
			wantErr: errForcedToolChoice,
		},
		{
			name:    "tool choice tool without a name",
			model:   "claude-opus-5",
			req:     model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired}},
			wantErr: errNoToolName,
		},
		{
			name:    "too many cache breakpoints",
			model:   "claude-opus-5-5",
			req:     model.Request{System: breakpoints, Messages: userText("hi").Messages},
			wantErr: errTooManyBreakpoints,
		},
		{
			name:    "an effort level the model lacks",
			model:   "claude-opus-4-6",
			req:     model.Request{Messages: userText("hi").Messages, Effort: model.EffortXHigh},
			wantMsg: `effort: "xhigh" is not supported by claude-opus-4-6`,
		},
		{
			name:    "a thinking mode the model lacks",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}},
			wantMsg: `thinking.mode: "enabled" is not supported by claude-opus-5-5`,
		},
		{
			name:    "thinking off on a model that always thinks",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingOff}},
			wantMsg: "always thinks",
		},
		{
			name:    "a tool call in a user message",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.ToolCall{ID: "t", Name: "n"}}}}},
			wantErr: errToolCallFromUser,
		},
		{
			name:    "a tool result in an assistant message",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: []model.Message{{Role: model.Assistant, Parts: []model.Part{model.ToolResult{CallID: "t"}}}}},
			wantErr: errToolResultFromBot,
		},
		{
			name:    "a tool schema that is not JSON",
			model:   "claude-opus-5-5",
			req:     model.Request{Messages: userText("hi").Messages, Tools: []model.ToolSpec{{Name: "bad", InputSchema: json.RawMessage(`{`)}}},
			wantMsg: "tool bad: input schema",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t)
			c := newTestConnector(t, api, tt.model, nil)

			_, err := c.Generate(t.Context(), tt.req)

			var merr *model.Error
			require.ErrorAs(t, err, &merr)
			require.Equal(t, model.ErrInvalid, merr.Kind)
			require.False(t, merr.Retryable())
			require.Empty(t, api.Requests(), "the request was sent")

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
			}
		})
	}
}

// TestReasoningIsSentBackUnchanged runs two turns: the thinking blocks of
// turn 1 must reach the API in turn 2 exactly as they came.
func TestReasoningIsSentBackUnchanged(t *testing.T) {
	t.Parallel()

	api := serve(t,
		recorded{status: http.StatusOK, file: "thinking.sse"},
		recorded{status: http.StatusOK, file: "text.sse"},
	)
	c := newTestConnector(t, api, "claude-haiku-4-5", func(c *Config) {
		c.Thinking = model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}
		c.MaxTokens = 8000
	})

	req := userText("Why did checkout fail?")

	first, err := c.Generate(t.Context(), req)
	require.NoError(t, err)

	req.Messages = append(req.Messages, first.Message, model.Message{Role: model.User, Parts: []model.Part{
		model.ToolResult{CallID: "toolu_01Deploys", Content: "deploy 812 at 13:58"},
	}})

	_, err = c.Generate(t.Context(), req)
	require.NoError(t, err)

	var messages []struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	require.NoError(t, json.Unmarshal(requestBody(t, api, 1)["messages"], &messages))
	require.Len(t, messages, 3)
	require.Equal(t, "assistant", messages[1].Role)
	require.Len(t, messages[1].Content, 3)

	var thinking struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}
	require.NoError(t, json.Unmarshal(messages[1].Content[0], &thinking))
	require.Equal(t, "thinking", thinking.Type)
	require.Equal(t, []byte(recordedThinking), []byte(thinking.Thinking))
	require.Equal(t, []byte(recordedSignature), []byte(thinking.Signature))

	var redacted struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(messages[1].Content[1], &redacted))
	require.Equal(t, "redacted_thinking", redacted.Type)
	require.Equal(t, []byte(recordedRedacted), []byte(redacted.Data))

	require.JSONEq(t, `{"id":"toolu_01Deploys","input":{"since": "13:30"},"name":"list_deploys","type":"tool_use"}`, string(messages[1].Content[2]))
}

func TestNew(t *testing.T) {
	t.Parallel()

	errNoFile := errors.New("open secret: no such file")

	tests := []struct {
		name       string
		cfg        Config
		noSecrets  bool
		resolveErr error
		wantErr    error
	}{
		{
			name: "known model",
			cfg:  Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef},
		},
		{
			name:      "no secrets resolver",
			cfg:       Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef},
			noSecrets: true,
			wantErr:   errNoSecrets,
		},
		{
			name:       "the API key cannot be read",
			cfg:        Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef},
			resolveErr: errNoFile,
			wantErr:    errNoFile,
		},
		{
			name:    "unknown model without capabilities",
			cfg:     Config{Settings: model.Settings{Model: "claude-unknown"}, APIKeySecret: apiKeyRef},
			wantErr: errUnknownModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)

			var deps registry.Deps

			if !tt.noSecrets {
				s := registry.NewMockSecrets(ctrl)
				s.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey), tt.resolveErr)
				deps.Secrets = s
			}

			c, err := New(t.Context(), "claude", tt.cfg, deps)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, knownModels[tt.cfg.Model].caps, c.Capabilities())

			named, ok := c.(interface{ ModelID() string })
			require.True(t, ok)
			require.Equal(t, tt.cfg.Model, named.ModelID())
		})
	}
}

func TestGenerateWhenTheKeyCannotBeRead(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey), nil),
		s.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return(nil, errors.New("secret removed")),
	)

	c, err := New(t.Context(), "claude", Config{Settings: model.Settings{Model: "claude-opus-5-5"}, APIKeySecret: apiKeyRef}, registry.Deps{Secrets: s})
	require.NoError(t, err)

	_, err = c.Generate(t.Context(), userText("hi"))

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrAuth, merr.Kind)
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
			section:  "model: claude-opus-5-5\neffort: high\napiKeySecret: {name: a, key: b}\n",
			wantCaps: knownModels["claude-opus-5-5"].caps,
		},
		{
			name:    "effort none is rejected",
			section: "model: claude-opus-5-5\neffort: none\napiKeySecret: {name: a, key: b}\n",
			wantErr: `effort: "none" is not supported by claude-opus-5-5`,
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

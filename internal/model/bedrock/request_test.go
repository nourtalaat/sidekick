package bedrock

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestGenerateRequestBody(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"LogQL"},"limit":{"type":"integer","maximum":1000}},"required":["query"]}`)
	temperature := 0.25

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
			model: opus55Geo,
			req:   userText("hi"),
			want: map[string]string{
				"messages":        `[{"role":"user","content":[{"text":"hi"}]}]`,
				"inferenceConfig": `{"maxTokens":64000}`,
			},
			absent: []string{"additionalModelRequestFields", "toolConfig", "system"},
		},
		{
			name:  "config settings apply when the request leaves them out; effort goes in output_config",
			model: opus5Global,
			edit: func(c *Config) {
				c.MaxTokens = 32000
				c.Effort = model.EffortHigh
				c.Thinking = model.Thinking{Mode: model.ThinkingAdaptive}
			},
			req: userText("hi"),
			want: map[string]string{
				"inferenceConfig":              `{"maxTokens":32000}`,
				"additionalModelRequestFields": `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
			},
		},
		{
			name:  "request settings win over config",
			model: opus5Global,
			edit:  func(c *Config) { c.MaxTokens = 32000; c.Effort = model.EffortHigh },
			req:   model.Request{Messages: userText("hi").Messages, MaxTokens: 30000, Effort: model.EffortLow, Temperature: &temperature},
			want: map[string]string{
				"inferenceConfig":              `{"maxTokens":30000,"temperature":0.25}`,
				"additionalModelRequestFields": `{"output_config":{"effort":"low"}}`,
			},
		},
		{
			name:  "effort alone, never inside thinking",
			model: opus55Geo,
			edit:  func(c *Config) { c.Effort = model.EffortXHigh },
			req:   userText("hi"),
			want:  map[string]string{"additionalModelRequestFields": `{"output_config":{"effort":"xhigh"}}`},
		},
		{
			name:  "output limit is capped at the model's maximum",
			model: "anthropic.claude-haiku-4-5-20251001-v1:0",
			req:   model.Request{Messages: userText("hi").Messages, MaxTokens: 100_000},
			want:  map[string]string{"inferenceConfig": `{"maxTokens":64000}`},
		},
		{
			name:  "thinking off is sent as disabled where the model takes it",
			model: opus5Global,
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingOff} },
			req:   userText("hi"),
			want:  map[string]string{"additionalModelRequestFields": `{"thinking":{"type":"disabled"}}`},
		},
		{
			name:   "thinking off is left out where off is the default",
			model:  opus48,
			edit:   func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingOff} },
			req:    userText("hi"),
			absent: []string{"additionalModelRequestFields"},
		},
		{
			name:  "a fixed thinking budget",
			model: sonnet46Geo,
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 4096} },
			req:   userText("hi"),
			want:  map[string]string{"additionalModelRequestFields": `{"thinking":{"type":"enabled","budget_tokens":4096}}`},
		},
		{
			name:  "cache points after system text, tools and message text",
			model: opus55Geo,
			req: model.Request{
				System: []model.Text{{Text: "You investigate alerts.", Cache: true}, {Text: "Today is Monday."}},
				Tools:  []model.ToolSpec{{Name: "search_logs", Description: "Search logs.", InputSchema: schema, Cache: true}, {Name: "list_deploys"}},
				Messages: []model.Message{{Role: model.User, Parts: []model.Part{
					model.Text{Text: "alert payload", Cache: true},
					model.Text{Text: "question"},
				}}},
			},
			want: map[string]string{
				"system": `[{"text":"You investigate alerts."},{"cachePoint":{"type":"default"}},{"text":"Today is Monday."}]`,
				"toolConfig": `{"tools":[` +
					`{"toolSpec":{"name":"search_logs","description":"Search logs.","inputSchema":{"json":` + string(schema) + `}}},` +
					`{"cachePoint":{"type":"default"}},` +
					`{"toolSpec":{"name":"list_deploys","inputSchema":{"json":{"type":"object","properties":{}}}}}]}`,
				"messages": `[{"role":"user","content":[{"text":"alert payload"},{"cachePoint":{"type":"default"}},{"text":"question"}]}]`,
			},
		},
		{
			name:  "cache flags are ignored for a model without prompt caching",
			model: novaDeclared,
			edit:  func(c *Config) { c.Declared = &DeclaredCapabilities{ToolCalling: true} },
			req:   model.Request{System: []model.Text{{Text: "You investigate alerts.", Cache: true}}, Messages: userText("hi").Messages},
			want:  map[string]string{"system": `[{"text":"You investigate alerts."}]`},
			absent: []string{
				"additionalModelRequestFields",
				"inferenceConfig",
			},
		},
		{
			name:  "tool choice tool reaches Converse on a model with thinking off",
			model: opus5Global,
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingOff} },
			req: model.Request{
				Messages:   userText("hi").Messages,
				Tools:      []model.ToolSpec{{Name: "submit_verdict", InputSchema: schema}},
				ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"},
			},
			want: map[string]string{
				"toolConfig":                   `{"tools":[{"toolSpec":{"name":"submit_verdict","inputSchema":{"json":` + string(schema) + `}}}],"toolChoice":{"tool":{"name":"submit_verdict"}}}`,
				"additionalModelRequestFields": `{"thinking":{"type":"disabled"}}`,
			},
		},
		{
			name:  "forced tool choice stays forced on a model that does not think by default",
			model: opus48,
			req: model.Request{
				Messages:   userText("hi").Messages,
				Tools:      []model.ToolSpec{{Name: "submit_verdict"}},
				ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny},
			},
			want: map[string]string{"toolConfig": `{"tools":[{"toolSpec":{"name":"submit_verdict","inputSchema":{"json":{"type":"object","properties":{}}}}}],"toolChoice":{"any":{}}}`},
		},
		{
			name:  "forced tool choice becomes auto while thinking is on",
			model: opus48,
			edit:  func(c *Config) { c.Thinking = model.Thinking{Mode: model.ThinkingAdaptive} },
			req: model.Request{
				Messages:   userText("hi").Messages,
				Tools:      []model.ToolSpec{{Name: "submit_verdict"}},
				ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"},
			},
			want: map[string]string{
				"toolConfig":                   `{"tools":[{"toolSpec":{"name":"submit_verdict","inputSchema":{"json":{"type":"object","properties":{}}}}}],"toolChoice":{"auto":{}}}`,
				"additionalModelRequestFields": `{"thinking":{"type":"adaptive"}}`,
			},
		},
		{
			name:  "forced tool choice becomes auto on a model that thinks by default",
			model: opus5Global,
			req: model.Request{
				Messages:   userText("hi").Messages,
				Tools:      []model.ToolSpec{{Name: "submit_verdict"}},
				ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"},
			},
			want:   map[string]string{"toolConfig": `{"tools":[{"toolSpec":{"name":"submit_verdict","inputSchema":{"json":{"type":"object","properties":{}}}}}],"toolChoice":{"auto":{}}}`},
			absent: []string{"additionalModelRequestFields"},
		},
		{
			name:  "tool calls and results, and reasoning from other providers dropped",
			model: opus55Geo,
			req: model.Request{Messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.Text{Text: "why?"}}},
				{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: "anthropic", Text: "thinking elsewhere", Signature: "sig-from-the-claude-api"},
					model.Reasoning{Provider: "openai", ID: "rs_1", Signature: "opaque"},
					model.ToolCall{ID: "tooluse_1", Name: "search_logs", Input: json.RawMessage(`{"query": "x", "limit": 12345678901234567890}`)},
				}},
				{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "tooluse_1", Content: "no data", IsError: true}}},
				{Role: model.Assistant, Parts: []model.Part{model.Reasoning{Provider: "openai", ID: "rs_2"}}},
			}},
			want: map[string]string{
				"messages": `[{"role":"user","content":[{"text":"why?"}]},` +
					`{"role":"assistant","content":[{"toolUse":{"toolUseId":"tooluse_1","name":"search_logs","input":{"query":"x","limit":12345678901234567890}}}]},` +
					`{"role":"user","content":[{"toolResult":{"toolUseId":"tooluse_1","content":[{"text":"no data"}],"status":"error"}}]}]`,
			},
		},
		{
			name:  "a tool error has no status for other model families",
			model: novaDeclared,
			edit:  func(c *Config) { c.Declared = &DeclaredCapabilities{ToolCalling: true} },
			req: model.Request{Messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "t1", Content: "failed", IsError: true}}},
			}},
			want: map[string]string{"messages": `[{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"text":"failed"}]}}]}]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.model, recorded{op: opStream, status: http.StatusOK, file: "text.jsonl"})
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

// TestSmallOutputUsesConverse checks that a call with a small output limit
// is not streamed, and one above the streaming threshold is.
func TestSmallOutputUsesConverse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maxTokens int
		response  recorded
	}{
		{name: "at the threshold", maxTokens: streamAbove, response: recorded{op: opConverse, status: http.StatusOK, file: "converse_tool_use.json"}},
		{name: "above the threshold", maxTokens: streamAbove + 1, response: recorded{op: opStream, status: http.StatusOK, file: "text.jsonl"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, opus55Geo, tt.response)
			c := newTestConnector(t, api, opus55Geo, func(c *Config) { c.MaxTokens = tt.maxTokens })

			_, err := c.Generate(t.Context(), userText("hi"))
			require.NoError(t, err)
		})
	}
}

func TestGenerateRejectsBeforeSending(t *testing.T) {
	t.Parallel()

	breakpoints := make([]model.Text, maxCachePoints+1)
	for i := range breakpoints {
		breakpoints[i] = model.Text{Text: "part", Cache: true}
	}

	tests := []struct {
		name    string
		model   string
		edit    func(*Config)
		req     model.Request
		wantErr error
		wantMsg string
	}{
		{
			name:    "tool choice none names the connector and the reason",
			model:   opus48,
			req:     model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			wantErr: errToolChoiceNone,
			wantMsg: "connector claude: tool choice none: the Converse API has no way to turn tools off",
		},
		{
			name:    "forced tool choice on a model that refuses it",
			model:   opus55Geo,
			req:     model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
			wantErr: errForcedToolChoice,
		},
		{
			name:    "tool choice tool without a name",
			model:   opus48,
			req:     model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired}},
			wantErr: errNoToolName,
		},
		{
			name:    "too many cache points",
			model:   opus55Geo,
			req:     model.Request{System: breakpoints, Messages: userText("hi").Messages},
			wantErr: errTooManyCachePoints,
		},
		{
			name:    "an effort level the model lacks",
			model:   "anthropic.claude-opus-4-6-v1",
			req:     model.Request{Messages: userText("hi").Messages, Effort: model.EffortXHigh},
			wantMsg: `effort: "xhigh" is not supported by anthropic.claude-opus-4-6-v1`,
		},
		{
			name:    "a thinking mode the model lacks",
			model:   opus55Geo,
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingEnabled, BudgetTokens: 2048}},
			wantMsg: `thinking.mode: "enabled" is not supported by us.anthropic.claude-opus-5-5`,
		},
		{
			name:    "thinking off on a model that always thinks",
			model:   opus55Geo,
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingOff}},
			wantMsg: "always thinks",
		},
		{
			name:    "thinking off with effort max where effort is capped at high",
			model:   opus5Global,
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingOff}, Effort: model.EffortMax},
			wantMsg: "only with effort high or lower",
		},
		{
			name:    "thinking on a model family without Claude's request shape",
			model:   novaDeclared,
			edit:    func(c *Config) { c.Declared = &DeclaredCapabilities{ToolCalling: true} },
			req:     model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingAdaptive}},
			wantMsg: `thinking.mode: "adaptive" is not supported by amazon.nova-pro-v1:0`,
		},
		{
			name:    "a tool call in a user message",
			model:   opus55Geo,
			req:     model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.ToolCall{ID: "t", Name: "n"}}}}},
			wantErr: errToolCallFromUser,
		},
		{
			name:    "a tool result in an assistant message",
			model:   opus55Geo,
			req:     model.Request{Messages: []model.Message{{Role: model.Assistant, Parts: []model.Part{model.ToolResult{CallID: "t"}}}}},
			wantErr: errToolResultFromBot,
		},
		{
			name:    "an unknown role",
			model:   opus55Geo,
			req:     model.Request{Messages: []model.Message{{Role: "system", Parts: []model.Part{model.Text{Text: "x"}}}}},
			wantErr: errUnknownRole,
		},
		{
			name:    "a tool schema that is not JSON",
			model:   opus55Geo,
			req:     model.Request{Messages: userText("hi").Messages, Tools: []model.ToolSpec{{Name: "bad", InputSchema: json.RawMessage(`{`)}}},
			wantMsg: "tool bad: input schema",
		},
		{
			name:  "a tool call input that is not JSON",
			model: opus55Geo,
			req: model.Request{Messages: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{model.ToolCall{ID: "t1", Name: "n", Input: json.RawMessage(`{"a":`)}}},
			}},
			wantMsg: "tool call t1: input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.model)
			c := newTestConnector(t, api, tt.model, tt.edit)

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

// TestReasoningIsSentBackUnchanged runs two turns: the reasoning of turn 1
// must reach Converse in turn 2 exactly as it came, in its place.
func TestReasoningIsSentBackUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model string
		file  string
		// want is the content of the assistant message in turn 2.
		want []string
	}{
		{
			name:  "reasoning text with a signature, and redacted reasoning",
			model: opus5Global,
			file:  "reasoning.jsonl",
			want: []string{
				`{"reasoningContent":{"reasoningText":{"text":` + jsonString(t, recordedThinking) + `,"signature":"` + recordedSignature + `"}}}`,
				`{"reasoningContent":{"redactedContent":"` + recordedRedacted + `"}}`,
				`{"toolUse":{"toolUseId":"tooluse_01Deploys","name":"list_deploys","input":{"since":"13:30"}}}`,
			},
		},
		{
			name:  "a signature with no text, as adaptive thinking returns by default",
			model: opus55Geo,
			file:  "reasoning_omitted.jsonl",
			want: []string{
				`{"reasoningContent":{"reasoningText":{"text":"","signature":"` + omittedSignature + `"}}}`,
				`{"toolUse":{"toolUseId":"tooluse_01Pods","name":"k8s__list_pods","input":{"namespace":"shop"}}}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.model,
				recorded{op: opStream, status: http.StatusOK, file: tt.file},
				recorded{op: opStream, status: http.StatusOK, file: "text.jsonl"},
			)
			c := newTestConnector(t, api, tt.model, nil)

			req := userText("Why did checkout fail?")

			first, err := c.Generate(t.Context(), req)
			require.NoError(t, err)

			call, ok := first.Message.Parts[len(first.Message.Parts)-1].(model.ToolCall)
			require.True(t, ok)

			req.Messages = append(req.Messages, first.Message, model.Message{Role: model.User, Parts: []model.Part{
				model.ToolResult{CallID: call.ID, Content: "deploy 812 at 13:58"},
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
			require.Len(t, messages[1].Content, len(tt.want))

			for i, want := range tt.want {
				require.JSONEq(t, want, string(messages[1].Content[i]), "content %d", i)
			}

			// The bytes of the reasoning text must be the same, not only
			// equal as JSON.
			var block struct {
				ReasoningContent struct {
					ReasoningText struct {
						Text      string `json:"text"`
						Signature string `json:"signature"`
					} `json:"reasoningText"`
				} `json:"reasoningContent"`
			}
			require.NoError(t, json.Unmarshal(messages[1].Content[0], &block))

			want, ok := first.Message.Parts[0].(model.Reasoning)
			require.True(t, ok)
			require.Equal(t, []byte(want.Text), []byte(block.ReasoningContent.ReasoningText.Text))
			require.Equal(t, []byte(want.Signature), []byte(block.ReasoningContent.ReasoningText.Signature))
		})
	}
}

// TestForcedToolChoiceIsLogged checks the debug line written when a forced
// tool choice is sent as auto.
func TestForcedToolChoiceIsLogged(t *testing.T) {
	t.Parallel()

	api := serve(t, opus5Global, recorded{op: opStream, status: http.StatusOK, file: "text.jsonl"})
	c := newTestConnector(t, api, opus5Global, nil)

	var logs lockedBuffer
	c.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const prompt = "secret-prompt-marker"

	_, err := c.Generate(t.Context(), model.Request{
		Messages:   userText(prompt).Messages,
		Tools:      []model.ToolSpec{{Name: "submit_verdict"}},
		ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"},
	})
	require.NoError(t, err)
	require.Contains(t, logs.String(), "sending tool choice auto")
	require.Contains(t, logs.String(), "tool_choice=tool")
	require.NotContains(t, logs.String(), prompt, "the log holds no prompt content")
}

// lockedBuffer is a bytes.Buffer that is safe for concurrent use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, err := b.buf.Write(p)

	return n, err //nolint:wrapcheck // bytes.Buffer.Write never fails.
}

// String returns what was written.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// jsonString returns s as a JSON string literal.
func jsonString(t *testing.T, s string) string {
	t.Helper()

	b, err := json.Marshal(s)
	require.NoError(t, err)

	return string(b)
}

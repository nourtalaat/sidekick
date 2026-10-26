package openai

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// gemmaOnVLLM is the Gemma 4 preset on vLLM, which takes a forced tool
// choice.
func gemmaOnVLLM() Config {
	return Config{Settings: model.Settings{Model: "google/gemma-4-31B-it"}, Server: ServerVLLM}
}

// qwenOnOllama is the Qwen3.8 preset under its Ollama tag, with no key.
func qwenOnOllama() Config {
	return Config{Settings: model.Settings{Model: "qwen3.8:27b"}}
}

func TestGenerateChat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		req      model.Request
		response recorded
		want     model.Response
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
	}{
		{
			name:     "text turn",
			cfg:      qwenOnOllama(),
			response: recorded{status: http.StatusOK, file: "chat_text.sse", headers: map[string]string{requestIDHeader: "req_01Chat"}},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The checkout service ran out of database connections."}}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 284, OutputTokens: 14, CacheReadTokens: 128},
				Model:      "qwen3.8:27b",
				RequestID:  "req_01Chat",
			},
		},
		{
			name:     "tool calls streamed over several chunks",
			cfg:      gemmaOnVLLM(),
			response: recorded{status: http.StatusOK, file: "chat_tool_calls.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.ToolCall{ID: "chatcmpl-tool-01", Name: "list_deploys", Input: json.RawMessage(`{"since": "13:30"}`)},
					model.ToolCall{ID: "chatcmpl-tool-02", Name: "search_logs", Input: json.RawMessage(`{"query": "level:error"}`)},
				}},
				StopReason: model.StopToolUse,
				Usage:      model.Usage{InputTokens: 900, OutputTokens: 61, ReasoningTokens: 20},
				Model:      "google/gemma-4-31B-it",
			},
		},
		{
			name:     "server ignores a forced tool choice and answers in text",
			cfg:      gemmaOnVLLM(),
			req:      model.Request{ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"}},
			response: recorded{status: http.StatusOK, file: "chat_ignored_tool_choice.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The pool was exhausted after the deploy."}}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 700, OutputTokens: 12},
				Model:      "google/gemma-4-31B-it",
			},
		},
		{
			name:     "output limit",
			cfg:      qwenOnOllama(),
			response: recorded{status: http.StatusOK, file: "chat_length.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The first cause is"}}},
				StopReason: model.StopMaxTokens,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 5},
				Model:      "qwen3.8:27b",
			},
		},
		{
			name:           "rate limited with retry-after",
			cfg:            qwenOnOllama(),
			response:       recorded{status: http.StatusTooManyRequests, file: "rate_limit.json", headers: map[string]string{model.RetryAfterHeader: "3"}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 3 * time.Second,
		},
		{
			name:     "vLLM context length error",
			cfg:      gemmaOnVLLM(),
			response: recorded{status: http.StatusBadRequest, file: "vllm_context_length.json"},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "llama.cpp context length error",
			cfg:      Config{Settings: model.Settings{Model: "Qwen/Qwen3.8-27B"}, Server: ServerLlamaCPP},
			response: recorded{status: http.StatusBadRequest, file: "llamacpp_context_length.json"},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "error part way through the stream",
			cfg:      gemmaOnVLLM(),
			response: recorded{status: http.StatusOK, file: "chat_stream_error.sse"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "stream with no answer",
			cfg:      qwenOnOllama(),
			response: recorded{status: http.StatusOK, file: "chat_empty.sse"},
			wantKind: model.ErrUnknown,
		},
		{
			name:     "stream that ends before the answer finished",
			cfg:      qwenOnOllama(),
			response: recorded{status: http.StatusOK, file: "chat_truncated.sse"},
			wantKind: model.ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, chatPath, tt.response)
			c := newTestConnector(t, api, tt.cfg)

			req := tt.req
			req.Messages = userText("Why did checkout fail?").Messages

			resp, err := c.Generate(t.Context(), req)
			sent := api.Requests()
			require.Len(t, sent, 1)
			require.Empty(t, sent[0].Header.Get("Authorization"))

			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, tt.wantRetryAfter)

				return
			}

			require.NoError(t, err)

			resp.Latency = 0
			require.Equal(t, tt.want, resp)
		})
	}
}

// TestGenerateChatOllamaToolCall checks Ollama's way of sending tool calls:
// the whole call in one chunk, finish reason stop, and possibly no ID.
func TestGenerateChatOllamaToolCall(t *testing.T) {
	t.Parallel()

	api := serve(t, chatPath, recorded{status: http.StatusOK, file: "chat_tool_call_ollama.sse"})
	c := newTestConnector(t, api, qwenOnOllama())

	resp, err := c.Generate(t.Context(), userText("Why did checkout fail?"))
	require.NoError(t, err)
	require.Equal(t, model.StopToolUse, resp.StopReason)
	require.Len(t, resp.Message.Parts, 1)

	call, ok := resp.Message.Parts[0].(model.ToolCall)
	require.True(t, ok)
	require.Equal(t, "list_deploys", call.Name)
	require.JSONEq(t, `{"since":"13:30"}`, string(call.Input))
	require.True(t, strings.HasPrefix(call.ID, callIDPrefix), "generated ID %q", call.ID)
}

func TestGenerateChatRequestBody(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)
	tools := []model.ToolSpec{{Name: "search_logs", Description: "Search logs.", InputSchema: schema}}

	tests := []struct {
		name string
		cfg  Config
		req  model.Request
		// want maps top-level keys of the request body to their JSON.
		want   map[string]string
		absent []string
	}{
		{
			name: "Ollama defaults: streamed with usage, no tool choice or limits",
			cfg:  qwenOnOllama(),
			req:  model.Request{Messages: userText("hi").Messages, Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAuto}},
			want: map[string]string{
				"model":          `"qwen3.8:27b"`,
				"stream":         `true`,
				"stream_options": `{"include_usage":true}`,
				"messages":       `[{"content":"hi","role":"user"}]`,
				"tools":          `[{"function":{"name":"search_logs","description":"Search logs.","parameters":{"type":"object","properties":{"query":{"type":"string"}}}},"type":"function"}]`,
			},
			absent: []string{"tool_choice", "max_tokens", "max_completion_tokens", "reasoning_effort", "temperature", "store", "metadata"},
		},
		{
			name:   "tool choice none on a server without tool choice leaves the tools out",
			cfg:    qwenOnOllama(),
			req:    model.Request{Messages: userText("hi").Messages, Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			absent: []string{"tools", "tool_choice"},
		},
		{
			name: "forced tool choice on vLLM",
			cfg:  gemmaOnVLLM(),
			req:  model.Request{Messages: userText("hi").Messages, Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "search_logs"}},
			want: map[string]string{"tool_choice": `{"function":{"name":"search_logs"},"type":"function"}`},
		},
		{
			name: "any tool on vLLM",
			cfg:  gemmaOnVLLM(),
			req:  model.Request{Messages: userText("hi").Messages, Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
			want: map[string]string{"tool_choice": `"required"`},
		},
		{
			name: "tool choice none on vLLM keeps the tools",
			cfg:  gemmaOnVLLM(),
			req:  model.Request{Messages: userText("hi").Messages, Tools: tools, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			want: map[string]string{"tool_choice": `"none"`},
		},
		{
			name: "effort, output limit and temperature on a compatible server",
			cfg: func() Config {
				c := qwenOnOllama()
				c.Effort = model.EffortLow
				c.MaxTokens = 4096

				return c
			}(),
			req:  model.Request{Messages: userText("hi").Messages, Temperature: new(0.6)},
			want: map[string]string{"reasoning_effort": `"low"`, "max_tokens": `4096`, "temperature": `0.6`},
		},
		{
			name: "conversation with system text, calls and results; reasoning dropped",
			cfg:  qwenOnOllama(),
			req: model.Request{
				System: []model.Text{{Text: "You investigate alerts."}},
				Messages: []model.Message{
					{Role: model.User, Parts: []model.Part{model.Text{Text: "why?"}}},
					{Role: model.Assistant, Parts: []model.Part{
						model.Reasoning{Provider: Provider, ID: "rs_1", Signature: "enc"},
						model.Text{Text: "Looking."},
						model.ToolCall{ID: "call_1", Name: "search_logs", Input: json.RawMessage(`{"query":"x"}`)},
					}},
					{Role: model.User, Parts: []model.Part{model.ToolResult{CallID: "call_1", Content: "no data"}, model.Text{Text: "Go on."}}},
					{Role: model.Assistant, Parts: []model.Part{model.Reasoning{Provider: "anthropic", Text: "t"}}},
				},
			},
			want: map[string]string{
				"messages": `[{"content":"You investigate alerts.","role":"system"},` +
					`{"content":"why?","role":"user"},` +
					`{"content":"Looking.","tool_calls":[{"id":"call_1","function":{"arguments":"{\"query\":\"x\"}","name":"search_logs"},"type":"function"}],"role":"assistant"},` +
					`{"content":"no data","tool_call_id":"call_1","role":"tool"},` +
					`{"content":"Go on.","role":"user"}]`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, chatPath, recorded{status: http.StatusOK, file: "chat_text.sse"})
			c := newTestConnector(t, api, tt.cfg)

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

// TestGenerateChatOpenAI checks Chat Completions on api.openai.com's
// model table, which takes max_completion_tokens.
func TestGenerateChatOpenAI(t *testing.T) {
	t.Parallel()

	api := serve(t, chatPath, recorded{status: http.StatusOK, file: "chat_text.sse"})

	cfg := Config{Settings: model.Settings{Model: "gpt-5.4", MaxTokens: 2000}, APIKeySecret: keyRef, API: APIChatCompletions}
	c := newTestConnector(t, api, cfg)
	// The test connector always has a base URL; clear it to take the path
	// of OpenAI's own API.
	c.cfg.BaseURL = ""

	_, err := c.Generate(t.Context(), userText("hi"))
	require.NoError(t, err)

	body := requestBody(t, api, 0)
	require.JSONEq(t, `2000`, string(body["max_completion_tokens"]))
	require.NotContains(t, body, "max_tokens")
	require.Equal(t, "Bearer "+testAPIKey, api.Requests()[0].Header.Get("Authorization"))
}

func TestGenerateChatRejectsForcedToolChoiceOnOllama(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode model.ToolChoiceMode
	}{
		{name: "any", mode: model.ToolChoiceAny},
		{name: "tool", mode: model.ToolChoiceRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, chatPath)
			c := newTestConnector(t, api, qwenOnOllama())

			req := userText("hi")
			req.ToolChoice = model.ToolChoice{Mode: tt.mode, Name: "submit_verdict"}

			_, err := c.Generate(t.Context(), req)
			requireKind(t, err, model.ErrInvalid, 0)
			require.ErrorIs(t, err, errForcedToolChoice)
			require.Empty(t, api.Requests())
		})
	}
}

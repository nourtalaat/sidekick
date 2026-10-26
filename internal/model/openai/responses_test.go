package openai

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Reasoning recorded in fixture/responses_tool_call.sse.
const (
	recordedReasoningID        = "rs_01Deploys"
	recordedEncryptedReasoning = "gAAAAABpZW5jcnlwdGVkLXJlYXNvbmluZy0wMQ=="
	recordedSummary            = "Check recent deploys first.\n\nThen the logs."
)

func TestGenerateResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response recorded
		want     model.Response
		wantKind model.ErrorKind
		// wantRetryAfter is the wait a failed call asks for.
		wantRetryAfter time.Duration
	}{
		{
			name:     "text turn",
			response: recorded{status: http.StatusOK, file: "responses_text.sse", headers: map[string]string{requestIDHeader: "req_01Text"}},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The checkout service ran out of database connections."}}},
				StopReason: model.StopEndTurn,
				Usage:      model.Usage{InputTokens: 412, OutputTokens: 14},
				Model:      "gpt-6-sol",
				RequestID:  "req_01Text",
			},
		},
		{
			name:     "reasoning and tool calls",
			response: recorded{status: http.StatusOK, file: "responses_tool_call.sse"},
			want: model.Response{
				Message: model.Message{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: Provider, ID: recordedReasoningID, Text: recordedSummary, Signature: recordedEncryptedReasoning},
					model.ToolCall{ID: "call_01Deploys", Name: "list_deploys", Input: json.RawMessage(`{"since":"13:30"}`)},
					model.ToolCall{ID: "call_02Logs", Name: "search_logs", Input: json.RawMessage(`{}`)},
				}},
				StopReason: model.StopToolUse,
				// 5200 input tokens hold 4096 read from the cache and 1024 written to it.
				Usage: model.Usage{InputTokens: 80, OutputTokens: 210, CacheReadTokens: 4096, CacheWriteTokens: 1024, ReasoningTokens: 150},
				Model: "gpt-6-sol",
			},
		},
		{
			name:     "refusal",
			response: recorded{status: http.StatusOK, file: "responses_refusal.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{}},
				StopReason: model.StopRefusal,
				Usage:      model.Usage{InputTokens: 80, OutputTokens: 7},
				Model:      "gpt-6-sol",
			},
		},
		{
			name:     "output limit",
			response: recorded{status: http.StatusOK, file: "responses_max_tokens.sse"},
			want: model.Response{
				Message:    model.Message{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "The first cause is"}}},
				StopReason: model.StopMaxTokens,
				Usage:      model.Usage{InputTokens: 300, OutputTokens: 5},
				Model:      "gpt-6-sol",
			},
		},
		{
			name:           "rate limited with retry-after",
			response:       recorded{status: http.StatusTooManyRequests, file: "rate_limit.json", headers: map[string]string{model.RetryAfterHeader: "20"}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 20 * time.Second,
		},
		{
			name:     "quota used up",
			response: recorded{status: http.StatusTooManyRequests, file: "quota.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "prompt too long",
			response: recorded{status: http.StatusBadRequest, file: "context_length.json"},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "bad API key",
			response: recorded{status: http.StatusUnauthorized, file: "auth.json"},
			wantKind: model.ErrAuth,
		},
		{
			name:     "server error",
			response: recorded{status: http.StatusInternalServerError, file: "server_error.json"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "invalid request",
			response: recorded{status: http.StatusBadRequest, file: "invalid.json"},
			wantKind: model.ErrInvalid,
		},
		{
			name:     "response failed part way",
			response: recorded{status: http.StatusOK, file: "responses_failed.sse"},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "error event in the stream",
			response: recorded{status: http.StatusOK, file: "responses_error_event.sse"},
			wantKind: model.ErrRateLimited,
		},
		{
			name:     "stream that ends early",
			response: recorded{status: http.StatusOK, file: "responses_truncated.sse"},
			wantKind: model.ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, responsesPath, tt.response)
			c := newTestConnector(t, api, responsesConfig("gpt-6-sol"))

			resp, err := c.Generate(t.Context(), userText("Why did checkout fail?"))
			sent := api.Requests()
			require.Len(t, sent, 1)
			require.Equal(t, "Bearer "+testAPIKey, sent[0].Header.Get("Authorization"))

			if tt.wantKind != "" {
				requireKind(t, err, tt.wantKind, tt.wantRetryAfter)

				return
			}

			require.NoError(t, err)
			require.Positive(t, resp.Latency)

			resp.Latency = 0
			require.Equal(t, tt.want, resp)
		})
	}
}

func TestGenerateResponsesRequestBody(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	temperature := 0.2

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
			name:  "defaults: streamed and stateless, no limits or effort",
			model: "gpt-6-sol",
			req:   userText("hi"),
			want: map[string]string{
				"model":  `"gpt-6-sol"`,
				"stream": `true`,
				"store":  `false`,
				"input":  `[{"content":"hi","role":"user"}]`,
			},
			absent: []string{"previous_response_id", "max_output_tokens", "reasoning", "tool_choice", "tools", "instructions", "temperature", "metadata", "include"},
		},
		{
			name:  "config settings apply when the request leaves them out",
			model: "gpt-6-astra",
			edit: func(c *Config) {
				c.MaxTokens = 16000
				c.Effort = model.EffortMax
				c.Temperature = &temperature
			},
			req: userText("hi"),
			want: map[string]string{
				"max_output_tokens": `16000`,
				"reasoning":         `{"effort":"max"}`,
				"temperature":       `0.2`,
			},
		},
		{
			name:  "request settings win, and the limit is capped at the model's",
			model: "gpt-5.4",
			edit:  func(c *Config) { c.MaxTokens = 16000; c.Effort = model.EffortHigh },
			req:   model.Request{Messages: userText("hi").Messages, MaxTokens: 500_000, Effort: model.EffortNone},
			want:  map[string]string{"max_output_tokens": `128000`, "reasoning": `{"effort":"none"}`},
		},
		{
			name:  "system text, tools, metadata and forced tool choice",
			model: "gpt-6-sol",
			req: model.Request{
				System:     []model.Text{{Text: "You investigate alerts.", Cache: true}, {Text: "Today is Monday."}},
				Tools:      []model.ToolSpec{{Name: "search_logs", Description: "Search logs.", InputSchema: schema, Cache: true}, {Name: "list_deploys"}},
				Messages:   userText("hi").Messages,
				ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired, Name: "submit_verdict"},
				Metadata:   map[string]string{model.MetadataRole: "investigate"},
			},
			want: map[string]string{
				"instructions": `"You investigate alerts.\n\nToday is Monday."`,
				"tools": `[{"strict":false,"parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},"name":"search_logs","description":"Search logs.","type":"function"},` +
					`{"strict":false,"parameters":{"type":"object","properties":{}},"name":"list_deploys","type":"function"}]`,
				"tool_choice": `{"name":"submit_verdict","type":"function"}`,
				"metadata":    `{"sidekick.role":"investigate"}`,
			},
		},
		{
			name:  "any tool",
			model: "gpt-6-sol",
			req:   model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceAny}},
			want:  map[string]string{"tool_choice": `"required"`},
		},
		{
			name:  "tool choice none",
			model: "gpt-6-sol",
			req:   model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceNone}},
			want:  map[string]string{"tool_choice": `"none"`},
		},
		{
			name:  "tool calls, results and reasoning in place; other providers' reasoning dropped",
			model: "gpt-6-sol",
			req: model.Request{Messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.Text{Text: "why?"}}},
				{Role: model.Assistant, Parts: []model.Part{
					model.Reasoning{Provider: "anthropic", Text: "thinking", Signature: "sig"},
					model.Reasoning{Provider: Provider, ID: "rs_1", Text: "Check deploys.", Signature: "enc-1"},
					model.Text{Text: "Looking at deploys."},
					model.ToolCall{ID: "call_1", Name: "list_deploys", Input: json.RawMessage(`{"since": "13:30"}`)},
					model.ToolCall{ID: "call_2", Name: "search_logs"},
				}},
				{Role: model.User, Parts: []model.Part{
					model.ToolResult{CallID: "call_1", Content: "deploy at 13:42"},
					model.ToolResult{CallID: "call_2", Content: "timeout", IsError: true},
					model.Text{Text: "Call submit_verdict now."},
				}},
			}},
			want: map[string]string{
				"input": `[{"content":"why?","role":"user"},` +
					`{"id":"rs_1","summary":[{"text":"Check deploys.","type":"summary_text"}],"encrypted_content":"enc-1","type":"reasoning"},` +
					`{"content":"Looking at deploys.","role":"assistant"},` +
					`{"arguments":"{\"since\": \"13:30\"}","call_id":"call_1","name":"list_deploys","type":"function_call"},` +
					`{"arguments":"{}","call_id":"call_2","name":"search_logs","type":"function_call"},` +
					`{"output":"deploy at 13:42","call_id":"call_1","type":"function_call_output"},` +
					`{"output":"timeout","call_id":"call_2","type":"function_call_output"},` +
					`{"content":"Call submit_verdict now.","role":"user"}]`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, responsesPath, recorded{status: http.StatusOK, file: "responses_text.sse"})

			cfg := responsesConfig(tt.model)
			if tt.edit != nil {
				tt.edit(&cfg)
			}

			c := newTestConnector(t, api, cfg)

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

// TestResponsesReplaysReasoning runs two turns and checks that the second
// sends the first turn's reasoning item back unchanged, before the calls
// that followed it, with no stored state.
func TestResponsesReplaysReasoning(t *testing.T) {
	t.Parallel()

	api := serve(t, responsesPath,
		recorded{status: http.StatusOK, file: "responses_tool_call.sse"},
		recorded{status: http.StatusOK, file: "responses_after_tools.sse"},
	)
	c := newTestConnector(t, api, responsesConfig("gpt-6-sol"))

	req := userText("Why did checkout fail?")

	first, err := c.Generate(t.Context(), req)
	require.NoError(t, err)

	req.Messages = append(req.Messages, first.Message, model.Message{Role: model.User, Parts: []model.Part{
		model.ToolResult{CallID: "call_01Deploys", Content: "deploy at 13:42"},
		model.ToolResult{CallID: "call_02Logs", Content: "pool exhausted"},
	}})

	second, err := c.Generate(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, model.StopEndTurn, second.StopReason)

	body := requestBody(t, api, 1)
	require.JSONEq(t, `false`, string(body["store"]))
	require.NotContains(t, body, "previous_response_id")

	var input []map[string]any
	require.NoError(t, json.Unmarshal(body["input"], &input))
	require.Len(t, input, 6)
	require.Equal(t, map[string]any{
		"type":              "reasoning",
		"id":                recordedReasoningID,
		"encrypted_content": recordedEncryptedReasoning,
		"summary":           []any{map[string]any{"type": "summary_text", "text": recordedSummary}},
	}, input[1])
	require.Equal(t, "function_call", input[2]["type"])
	require.Equal(t, "call_01Deploys", input[2]["call_id"])
	require.Equal(t, "function_call", input[3]["type"])
	require.Equal(t, "function_call_output", input[4]["type"])
}

func TestGenerateResponsesRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  model.Request
	}{
		{name: "an effort the model lacks", req: model.Request{Messages: userText("hi").Messages, Effort: model.EffortMinimal}},
		{name: "a thinking mode", req: model.Request{Messages: userText("hi").Messages, Thinking: model.Thinking{Mode: model.ThinkingAdaptive}}},
		{name: "tool choice tool without a name", req: model.Request{Messages: userText("hi").Messages, ToolChoice: model.ToolChoice{Mode: model.ToolChoiceRequired}}},
		{name: "a tool call from the user", req: model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.ToolCall{ID: "c", Name: "x"}}}}}},
		{name: "a tool result from the model", req: model.Request{Messages: []model.Message{{Role: model.Assistant, Parts: []model.Part{model.ToolResult{CallID: "c"}}}}}},
		{name: "an unknown role", req: model.Request{Messages: []model.Message{{Role: "system", Parts: []model.Part{model.Text{Text: "x"}}}}}},
		{name: "a tool schema that is not JSON", req: model.Request{Messages: userText("hi").Messages, Tools: []model.ToolSpec{{Name: "x", InputSchema: json.RawMessage(`{`)}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, responsesPath)
			c := newTestConnector(t, api, responsesConfig("gpt-6.1-sol"))

			_, err := c.Generate(t.Context(), tt.req)
			requireKind(t, err, model.ErrInvalid, 0)
			require.Empty(t, api.Requests())
		})
	}
}

package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/anthropic"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
)

// judgePromptSum is the SHA-256 of judge.md at the current
// JudgePromptVersion.
const judgePromptSum = "942855196c71d9d16f488fb13a84a071b66791e06e2158e8c5cfdde1b5bde767"

// judgeMessagesPath is the path of the Anthropic Messages API.
const judgeMessagesPath = "/v1/messages"

// Anthropic error bodies the judge tests serve.
const (
	invalidRequestBody = `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`
	overloadedBody     = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	statusOverloaded   = 529
)

// judgeKeyRef is the secret reference of the judge tests' connector.
var judgeKeyRef = registry.SecretRef{Name: "anthropic", Key: "apiKey"}

// judgePrices prices claude-opus-5 at $5 and $25 per million input and
// output tokens, in micro-dollars.
var judgePrices = pricing.New(map[string]model.Price{"claude-opus-5": {InputPerMTok: 5_000_000, OutputPerMTok: 25_000_000}})

// gradedPool is a report that finds the pool exhaustion and commit abc123.
var gradedPool = report.Report{
	Verdict: report.TruePositive, Confidence: report.High,
	Summary: "The DB pool ran out after abc123.", Reason: "Latency rose after abc123.",
	Cause: "Commit abc123 lowered the pool size to 5.", Impact: "Checkout p99 above 2s.",
	Findings: []report.Finding{{ID: "f1", Claim: "abc123 lowered the pool size."}},
}

func TestJudgeGrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// modelID is the Anthropic model the judge uses.
		modelID   string
		responses []fakehttp.Response
		wantErr   error
		// wantPlainErr is an error that must not be an infrastructure one.
		wantPlainErr string
		want         Grade
		check        func(t *testing.T, api *fakehttp.Server)
	}{
		{
			name: "grades in one turn, with submit_grades forced",
			// Claude Opus 4.8 takes a forced tool choice and does not think
			// unless asked to.
			modelID:   "claude-opus-4-8",
			responses: []fakehttp.Response{sseFile("fixture/judge-grades.sse")},
			want: Grade{
				Items: []ItemGrade{
					{ID: "pool", Met: true, Reason: "The cause names the exhausted pool."},
					{ID: "commit", Met: true, Reason: "It links the cause to commit abc123."},
					{ID: "fix", Reason: "No fix restores the pool size."},
				},
				InputTokens: 1200, OutputTokens: 80, CostMicros: 8000,
			},
			check: func(t *testing.T, api *fakehttp.Server) {
				t.Helper()

				body := judgeRequest(t, api, 0)
				require.JSONEq(t, `{"type": "tool", "name": "submit_grades"}`, string(body["tool_choice"]))
				require.NotContains(t, body, "temperature", "the Anthropic API takes no temperature")
				require.Contains(t, string(body["messages"]), "untrusted_data")
				require.Contains(t, string(body["messages"]), "pool (weight 2, required): Names pool exhaustion")
			},
		},
		{
			name:      "a model that cannot be forced gets tool choice auto",
			modelID:   "claude-opus-5-5",
			responses: []fakehttp.Response{sseFile("fixture/judge-grades.sse")},
			want: Grade{
				Items: []ItemGrade{
					{ID: "pool", Met: true, Reason: "The cause names the exhausted pool."},
					{ID: "commit", Met: true, Reason: "It links the cause to commit abc123."},
					{ID: "fix", Reason: "No fix restores the pool size."},
				},
				InputTokens: 1200, OutputTokens: 80, CostMicros: 8000,
			},
			check: func(t *testing.T, api *fakehttp.Server) {
				t.Helper()

				body := judgeRequest(t, api, 0)
				require.NotContains(t, string(body["tool_choice"]), `"tool"`)
			},
		},
		{
			name:    "grades of unknown items go back to the judge",
			modelID: "claude-opus-5",
			responses: []fakehttp.Response{
				sseFile("fixture/judge-unknown-item.sse"),
				sseFile("fixture/judge-grades.sse"),
			},
			want: Grade{
				Items: []ItemGrade{
					{ID: "pool", Met: true, Reason: "The cause names the exhausted pool."},
					{ID: "commit", Met: true, Reason: "It links the cause to commit abc123."},
					{ID: "fix", Reason: "No fix restores the pool size."},
				},
				InputTokens: 2400, OutputTokens: 140, CostMicros: 15500,
			},
			check: func(t *testing.T, api *fakehttp.Server) {
				t.Helper()

				messages := string(judgeRequest(t, api, 1)["messages"])
				require.Contains(t, messages, "toolu_02Unknown")
				require.Contains(t, messages, `\"network\" is not a rubric point`)
				require.Contains(t, messages, `\"commit\" has no grade`)
				require.Contains(t, messages, `"is_error":true`)
			},
		},
		{
			name:    "a turn without submit_grades gets a reminder",
			modelID: "claude-opus-5",
			responses: []fakehttp.Response{
				sseFile("fixture/judge-text.sse"),
				sseFile("fixture/judge-grades.sse"),
			},
			want: Grade{
				Items: []ItemGrade{
					{ID: "pool", Met: true, Reason: "The cause names the exhausted pool."},
					{ID: "commit", Met: true, Reason: "It links the cause to commit abc123."},
					{ID: "fix", Reason: "No fix restores the pool size."},
				},
				InputTokens: 2400, OutputTokens: 100, CostMicros: 14500,
			},
			check: func(t *testing.T, api *fakehttp.Server) {
				t.Helper()

				require.Contains(t, string(judgeRequest(t, api, 1)["messages"]), noGradesText)
			},
		},
		{
			name:    "no valid grades in the attempts",
			modelID: "claude-opus-5",
			responses: []fakehttp.Response{
				sseFile("fixture/judge-text.sse"),
				sseFile("fixture/judge-text.sse"),
				sseFile("fixture/judge-text.sse"),
			},
			wantPlainErr: errNoGrades.Error(),
		},
		{
			name:         "a request the provider refuses",
			modelID:      "claude-opus-5",
			responses:    []fakehttp.Response{jsonError(http.StatusBadRequest, invalidRequestBody)},
			wantPlainErr: "the judge model refused the request (invalid_request)",
		},
		{
			name:      "a provider that stays overloaded",
			modelID:   "claude-opus-5",
			responses: []fakehttp.Response{jsonError(statusOverloaded, overloadedBody)},
			wantErr:   ErrProviderUnreachable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, judgeMessagesPath, tt.responses...)
			api.ExpectAllCalled()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), judgeKeyRef).Return([]byte("test-key-not-real\n"), nil).AnyTimes()

			conn, err := anthropic.New(t.Context(), "judge", anthropic.Config{
				Settings:     model.Settings{Model: tt.modelID},
				APIKeySecret: judgeKeyRef,
				BaseURL:      api.URL(),
			}, registry.Deps{Secrets: secrets, HTTP: api.Client()})
			require.NoError(t, err)

			// The recorded answers come from claude-opus-5, which prices them.
			judge, err := NewJudge(JudgeOptions{Connector: conn, Name: "judge", Prices: judgePrices})
			require.NoError(t, err)

			got, err := judge.Grade(t.Context(), RootCause{Summary: "The pool ran out after abc123.", Rubric: poolRubric}, gradedPool)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantPlainErr != "":
				require.ErrorContains(t, err, tt.wantPlainErr)
				require.NotErrorIs(t, err, ErrInfrastructure)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}

			if tt.check != nil {
				tt.check(t, api)
			}
		})
	}
}

func TestJudgeTemperature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		caps            model.Capabilities
		wantTemperature *float64
	}{
		{name: "a model that takes a temperature grades at 0", caps: model.Capabilities{ToolCalling: true, Temperature: true}, wantTemperature: new(0.0)},
		{name: "a model that takes none gets none", caps: model.Capabilities{ToolCalling: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grades := `{"grades": [{"id": "pool", "met": true, "reason": "r"}, {"id": "commit", "met": false, "reason": "r"}, {"id": "fix", "met": false, "reason": "r"}]}`
			conn := modeltest.New(t, tt.caps, modeltest.ToolUse(model.Usage{InputTokens: 10}, modeltest.Call("g1", SubmitGradesTool, grades)))
			conn.ExpectAllUsed()

			judge, err := NewJudge(JudgeOptions{Connector: conn})
			require.NoError(t, err)

			got, err := judge.Grade(t.Context(), RootCause{Rubric: poolRubric}, gradedPool)
			require.NoError(t, err)
			require.Len(t, got.Items, len(poolRubric))

			req := conn.Requests()[0]
			require.Equal(t, tt.wantTemperature, req.Temperature)
			require.Equal(t, string(model.RoleJudge), req.Metadata[model.MetadataRole])
			require.Equal(t, model.ToolChoiceMode(""), req.ToolChoice.Mode, "the connector cannot force a tool")
		})
	}
}

func TestJudgePromptVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "judge.md matches JudgePromptVersion"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sum := sha256.Sum256([]byte(judgePrompt))
			require.Equal(t, judgePromptSum, hex.EncodeToString(sum[:]),
				"judge.md changed: change JudgePromptVersion, then set judgePromptSum to the new sum")
		})
	}
}

// sseFile is a recorded streaming answer of the Messages API.
func sseFile(path string) fakehttp.Response {
	return fakehttp.Response{BodyFile: path, Headers: map[string]string{"Content-Type": "text/event-stream"}}
}

// jsonError is an error answer of the Messages API.
func jsonError(status int, body string) fakehttp.Response {
	return fakehttp.Response{Status: status, Body: []byte(body), Headers: map[string]string{"Content-Type": "application/json"}}
}

// judgeRequest returns the JSON body of request i that api received.
func judgeRequest(t *testing.T, api *fakehttp.Server, i int) map[string]json.RawMessage {
	t.Helper()

	requests := api.Requests()
	require.Greater(t, len(requests), i)

	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(requests[i].Body, &body))

	return body
}

package gcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Paths of the fake APIs.
const (
	entriesPath = "/logging/v2/entries:list"
)

// The default windows of the tools at testNow.
var (
	lastHour = window{start: at(9, 0), end: testNow}
	lastDay  = window{start: testNow.Add(-24 * time.Hour), end: testNow}
)

// changeLogFilter is the audit log filter of list_changes for shop-prod.
const changeLogFilter = `(logName="projects/shop-prod/logs/cloudaudit.googleapis.com%2Factivity" OR ` +
	`logName="projects/shop-prod/logs/cloudaudit.googleapis.com%2Fsystem_event")`

func TestSourceCall(t *testing.T) {
	t.Parallel()

	errorFilter := "(\nseverity>=ERROR\n)\nAND timestamp>=\"2026-05-04T09:00:00Z\" AND timestamp<\"2026-05-04T10:00:00Z\""
	changesFilter := "(\n" + changeLogFilter + ` AND protoPayload.serviceName="run.googleapis.com"` +
		"\n)\nAND timestamp>=\"2026-05-03T10:00:00Z\" AND timestamp<\"2026-05-04T10:00:00Z\""

	tests := []struct {
		name   string
		tool   string
		input  string
		routes []route
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries []string
		// wantBodies are the JSON bodies of the requests, in order.
		wantBodies    []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:  "query_logs searches the last hour with the filter as given",
			tool:  toolQueryLogs,
			input: `{"filter":"severity>=ERROR"}`,
			routes: []route{{http.MethodPost, entriesPath, []fakehttp.Response{body(`{"entries":[
				{"logName":"projects/shop-prod/logs/stderr","resource":{"type":"k8s_container","labels":{"cluster_name":"prod","container_name":"checkout"}},
				 "timestamp":"2026-05-04T09:58:00Z","severity":"ERROR","textPayload":"payment timeout\n","trace":"projects/shop-prod/traces/abc"},
				{"logName":"projects/shop-prod/logs/run.googleapis.com%2Frequests","resource":{"type":"cloud_run_revision","labels":{"service_name":"checkout"}},
				 "timestamp":"2026-05-04T09:57:00Z","severity":"ERROR","jsonPayload":{ "message": "db down", "code": 500 },
				 "httpRequest":{"requestMethod":"POST","requestUrl":"https://checkout.example.com/pay","status":503,"latency":"2.5s"}}
			]}`)}}},
			wantBodies: []string{`{"resourceNames":["projects/shop-prod"],"orderBy":"timestamp desc","pageSize":100,"filter":` +
				mustJSON(t, errorFilter) + `}`},
			want: `{"project":"shop-prod","entries":[
				{"time":"2026-05-04T09:58:00Z","severity":"ERROR","log":"stderr","resource":{"type":"k8s_container","labels":{"cluster_name":"prod","container_name":"checkout"}},
				 "message":"payment timeout","trace":"projects/shop-prod/traces/abc"},
				{"time":"2026-05-04T09:57:00Z","severity":"ERROR","log":"run.googleapis.com/requests","resource":{"type":"cloud_run_revision","labels":{"service_name":"checkout"}},
				 "message":"{\"message\":\"db down\",\"code\":500}",
				 "httpRequest":{"requestMethod":"POST","requestUrl":"https://checkout.example.com/pay","status":503,"latency":"2.5s"}}
			]}`,
			wantEvidence: []source.Evidence{{
				Kind:  source.EvidenceLogQuery,
				Title: "Cloud Logging query in shop-prod",
				URL: "https://console.cloud.google.com/logs/query;query=%28%0Aseverity%3E%3DERROR%0A%29%0AAND%20timestamp%3E%3D%222026-05-04T09%3A00%3A00Z%22" +
					"%20AND%20timestamp%3C%222026-05-04T10%3A00%3A00Z%22;timeRange=2026-05-04T09%3A00%3A00Z%2F2026-05-04T10%3A00%3A00Z?project=shop-prod",
				At: testNow,
			}},
		},
		{
			name:  "query_logs pages up to the limit and says when more are left",
			tool:  toolQueryLogs,
			input: `{"limit":3}`,
			routes: []route{{http.MethodPost, entriesPath, []fakehttp.Response{
				body(`{"entries":[{"logName":"projects/shop-prod/logs/a","timestamp":"2026-05-04T09:58:00Z","textPayload":"one"},
					{"logName":"projects/shop-prod/logs/a","timestamp":"2026-05-04T09:57:00Z","textPayload":"two"}],"nextPageToken":"p2"}`),
				body(`{"entries":[{"logName":"projects/shop-prod/logs/a","timestamp":"2026-05-04T09:56:00Z","textPayload":"three"}],"nextPageToken":"p3"}`),
			}}},
			wantBodies: []string{
				`{"resourceNames":["projects/shop-prod"],"orderBy":"timestamp desc","pageSize":3,"filter":` + mustJSON(t, withTime("", lastHour)) + `}`,
				`{"resourceNames":["projects/shop-prod"],"orderBy":"timestamp desc","pageSize":1,"pageToken":"p2","filter":` + mustJSON(t, withTime("", lastHour)) + `}`,
			},
			want: `{"project":"shop-prod","entries":[
				{"time":"2026-05-04T09:58:00Z","log":"a","resource":{"type":""},"message":"one"},
				{"time":"2026-05-04T09:57:00Z","log":"a","resource":{"type":""},"message":"two"},
				{"time":"2026-05-04T09:56:00Z","log":"a","resource":{"type":""},"message":"three"}
			]}`,
			wantEvidence:  []source.Evidence{logsLink(source.EvidenceLogQuery, "Cloud Logging query in shop-prod", withTime("", lastHour), lastHour)},
			wantTruncated: true,
		},
		{
			name:         "query_logs with no entries is no data with a link to the query",
			tool:         toolQueryLogs,
			input:        `{"project":"shop-prod","filter":"severity>=ERROR"}`,
			routes:       []route{{http.MethodPost, entriesPath, []fakehttp.Response{body(`{}`)}}},
			wantNoData:   true,
			wantText:     "No log entries in shop-prod matched between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{logsLink(source.EvidenceLogQuery, "Cloud Logging query in shop-prod", errorFilter, lastHour)},
		},
		{
			name:          "a filter Cloud Logging cannot parse is a tool error",
			tool:          toolQueryLogs,
			input:         `{"filter":"severity>>"}`,
			routes:        []route{{http.MethodPost, entriesPath, []fakehttp.Response{failed(http.StatusBadRequest, "INVALID_ARGUMENT", "Unparseable filter")}}},
			wantToolError: "Google Cloud answered 400 INVALID_ARGUMENT: Unparseable filter",
		},
		{
			name:          "a permission the identity lacks is a tool error",
			tool:          toolQueryLogs,
			input:         `{}`,
			routes:        []route{{http.MethodPost, entriesPath, []fakehttp.Response{failed(http.StatusForbidden, "PERMISSION_DENIED", "logging.logEntries.list denied")}}},
			wantToolError: "Google Cloud answered 403 PERMISSION_DENIED: logging.logEntries.list denied",
		},
		{
			name:    "a refused token is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{}`,
			routes:  []route{{http.MethodPost, entriesPath, []fakehttp.Response{failed(http.StatusUnauthorized, "UNAUTHENTICATED", "bad token")}}},
			wantErr: "Google Cloud answered 401 UNAUTHENTICATED: bad token",
		},
		{
			name:    "a rate limit is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{}`,
			routes:  []route{{http.MethodPost, entriesPath, []fakehttp.Response{failed(http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "quota")}}},
			wantErr: "Google Cloud answered 429 RESOURCE_EXHAUSTED: quota",
		},
		{
			name:    "a server error without a body is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{}`,
			routes:  []route{{http.MethodPost, entriesPath, []fakehttp.Response{{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")}}}},
			wantErr: "Google Cloud answered 502",
		},
		{
			name:    "an answer that is not JSON is a failure of the source",
			tool:    toolQueryLogs,
			input:   `{}`,
			routes:  []route{{http.MethodPost, entriesPath, []fakehttp.Response{body("{")}}},
			wantErr: "decode the answer",
		},
		{
			name:  "list_changes reads the audit logs of one service",
			tool:  toolListChanges,
			input: `{"service":"run.googleapis.com"}`,
			routes: []route{{http.MethodPost, entriesPath, []fakehttp.Response{body(`{"entries":[
				{"logName":"projects/shop-prod/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"2026-05-04T09:30:00Z","severity":"NOTICE",
				 "protoPayload":{"@type":"type.googleapis.com/google.cloud.audit.AuditLog","serviceName":"run.googleapis.com",
				  "methodName":"google.cloud.run.v2.Services.UpdateService","resourceName":"projects/shop-prod/locations/europe-west1/services/checkout",
				  "authenticationInfo":{"principalEmail":"deploy@shop-prod.iam.gserviceaccount.com"}}},
				{"logName":"projects/shop-prod/logs/cloudaudit.googleapis.com%2Factivity","timestamp":"2026-05-04T09:10:00Z","severity":"ERROR",
				 "protoPayload":{"serviceName":"run.googleapis.com","methodName":"google.cloud.run.v2.Services.DeleteService",
				  "resourceName":"projects/shop-prod/locations/europe-west1/services/checkout","status":{"code":7,"message":"Permission denied"}}}
			]}`)}}},
			wantBodies: []string{`{"resourceNames":["projects/shop-prod"],"orderBy":"timestamp desc","pageSize":100,"filter":` + mustJSON(t, changesFilter) + `}`},
			want: `{"project":"shop-prod","changes":[
				{"time":"2026-05-04T09:30:00Z","log":"cloudaudit.googleapis.com/activity","service":"run.googleapis.com",
				 "method":"google.cloud.run.v2.Services.UpdateService","resource":"projects/shop-prod/locations/europe-west1/services/checkout",
				 "principal":"deploy@shop-prod.iam.gserviceaccount.com","severity":"NOTICE"},
				{"time":"2026-05-04T09:10:00Z","log":"cloudaudit.googleapis.com/activity","service":"run.googleapis.com",
				 "method":"google.cloud.run.v2.Services.DeleteService","resource":"projects/shop-prod/locations/europe-west1/services/checkout",
				 "severity":"ERROR","error":"code 7: Permission denied"}
			]}`,
			wantEvidence: []source.Evidence{logsLink(source.EvidenceDeploy, "Audit log changes in shop-prod", changesFilter, lastDay)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			for _, r := range tt.routes {
				api.Handle(r.method, r.path, r.responses...)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api)

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			requests := api.Requests()
			for i, req := range requests {
				require.Equal(t, "Bearer "+testToken, req.Header.Get("Authorization"))
				require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "sidekick/"))

				if i < len(tt.wantQueries) {
					require.Equal(t, tt.wantQueries[i], req.Query.Encode(), "query of request %d", i)
				}

				if i < len(tt.wantBodies) {
					require.JSONEq(t, tt.wantBodies[i], string(req.Body), "body of request %d", i)
				}
			}

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)

				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "a failure of the source is not a tool error")
			case tt.wantNoData:
				require.NoError(t, err)
				require.True(t, res.NoData)
				require.Equal(t, tt.wantText, res.Text)
				require.Equal(t, tt.wantEvidence, res.Evidence)
				require.Empty(t, res.Structured)
			default:
				require.NoError(t, err)
				require.JSONEq(t, tt.want, string(res.Structured))
				require.Equal(t, tt.wantEvidence, res.Evidence)
				require.Equal(t, tt.wantTruncated, res.Truncated)
				require.False(t, res.NoData)
			}
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []string
		tool     string
		input    string
		want     string
	}{
		{name: "a project the config does not list", tool: toolQueryLogs, input: `{"project":"other"}`,
			want: `project "other" is not one Sidekick may read; allowed: shop-prod`},
		{name: "no project with several in the config", projects: []string{"shop-prod", "shop-staging"}, tool: toolListChanges, input: `{}`,
			want: "give project; allowed: shop-prod, shop-staging"},
		{name: "a range longer than seven days", tool: toolQueryLogs, input: `{"since":"2026-04-01T00:00:00Z"}`,
			want: "longer than the 168h0m0s one call may read"},
		{name: "since after until", tool: toolQueryLogs, input: `{"since":"2026-05-04T11:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			want: "since must be before until"},
		{name: "a time that does not parse", tool: toolListChanges, input: `{"since":"yesterday"}`,
			want: `since: "yesterday" is not an RFC 3339 time`},
		{name: "a service that is not a Google API", tool: toolListChanges, input: `{"service":"run\" OR true"}`,
			want: "is not a Google API name"},
		{name: "a resource with a quote", tool: toolListChanges, input: `{"resource":"checkout\" OR logName:\"x"}`,
			want: "without quotes, backslashes or line breaks"},
		{name: "input that does not decode", tool: toolQueryLogs, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, tt.projects...)

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Google Cloud")
		})
	}
}

func TestSourceCallCutsLongResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries int
		message int
	}{
		{name: "a long message is cut", entries: 1, message: maxMessageLen + 100},
		{name: "a result stops before it grows too large", entries: maxEntries, message: maxMessageLen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entries := make([]map[string]any, tt.entries)
			for i := range entries {
				entries[i] = map[string]any{
					"logName":     "projects/shop-prod/logs/app",
					"timestamp":   "2026-05-04T09:58:00Z",
					"textPayload": strings.Repeat("é", tt.message/2),
				}
			}

			data, err := json.Marshal(map[string]any{"entries": entries})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, entriesPath, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api)

			res, err := s.Call(t.Context(), source.Call{Tool: toolQueryLogs, Input: json.RawMessage(`{"limit":500}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)
			// The budget counts the items; the commas between them and the
			// project around them come on top.
			require.LessOrEqual(t, len(res.Structured), maxResultSize+tt.entries+len(`{"project":"shop-prod","entries":[]}`))

			var got logsResult
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.NotEmpty(t, got.Entries)

			for _, e := range got.Entries {
				require.LessOrEqual(t, len(e.Message), maxMessageLen)
			}
		})
	}
}

func TestSourceTokenFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "no credentials", err: errors.New("could not find default credentials")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api)
			s.client.tokens = failingTokens{err: tt.err}

			_, err := s.Call(t.Context(), source.Call{Tool: toolQueryLogs, Input: json.RawMessage(`{}`)})
			require.ErrorContains(t, err, "get an access token")
			require.ErrorIs(t, err, tt.err)

			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.False(t, isToolErr)
			require.Empty(t, api.Requests(), "no request is sent without a token")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "delete_log"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	ref := &registry.SecretRef{Name: "sidekick-gcp", Key: "credentials.json"}

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
	}{
		{name: "a credentials secret without a secrets resolver", cfg: Config{Projects: []string{testProject}, CredentialsSecret: ref},
			wantErr: errNoSecrets.Error()},
		{name: "settings that do not check", cfg: Config{Projects: []string{testProject}, Settings: source.Settings{Concurrency: -1}},
			wantErr: "concurrency: must not be negative"},
		{name: "default credentials need no secrets resolver", cfg: Config{Projects: []string{testProject}}},
		{name: "a credentials secret with a secrets resolver", cfg: Config{Projects: []string{testProject}, CredentialsSecret: ref}, withSecrets: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			s, err := New(t.Context(), "gcp", tt.cfg, deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, s)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, s.Close())
		})
	}
}

// mustJSON returns s as a JSON string.
func mustJSON(t *testing.T, s string) string {
	t.Helper()

	data, err := json.Marshal(s)
	require.NoError(t, err)

	return string(data)
}

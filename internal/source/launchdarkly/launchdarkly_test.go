package launchdarkly

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// Paths of the fake API.
const (
	flagPath = "/api/v2/flags/shop/new-checkout"
)

// Queries of the audit log requests, for the last hour of the test day.
const (
	// everyFlagQuery reads every flag of shop/production, 20 entries.
	everyFlagQuery = "after=1777885199999&before=1777888800000&limit=20&spec=proj%2Fshop%3Aenv%2Fproduction%3Aflag%2F%2A"
	// oneFlagQuery reads new-checkout in shop/staging, 4 entries.
	oneFlagQuery = "after=1777885199999&before=1777888800000&limit=4&spec=proj%2Fshop%3Aenv%2Fstaging%3Aflag%2Fnew-checkout"
)

// Evidence links of the tests.
var (
	newCheckoutLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "LaunchDarkly flag new-checkout in shop/production",
		URL: appURL + "/shop/production/features/new-checkout"}
	darkModeLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "LaunchDarkly flag dark-mode in shop/production",
		URL: appURL + "/shop/production/features/dark-mode"}
	productionLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "LaunchDarkly flags in shop/production",
		URL: appURL + "/shop/production/features"}
)

func TestSourceCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tool     string
		input    string
		path     string
		response []fakehttp.Response
		// wantQueries are the encoded queries of the requests, in order.
		wantQueries   []string
		want          string
		wantEvidence  []source.Evidence
		wantTruncated bool
		wantNoData    bool
		wantText      string
		wantToolError string
		wantErr       string
	}{
		{
			name:        "list_flag_changes reads every flag of the environment",
			tool:        flags.ToolListChanges,
			input:       `{"project":"shop","environment":"production","since":"2026-05-04T09:00:00Z"}`,
			path:        auditLogPath,
			response:    []fakehttp.Response{fixture("auditlog.json")},
			wantQueries: []string{everyFlagQuery},
			want: `{"project":"shop","environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","changes":[
				{"time":"2026-05-04T09:58:00Z","flag":"new-checkout","name":"New checkout","action":"turned on the flag",
				 "actor":"ada@example.com","summary":"- Turned on the flag","comment":"Rolling out to everyone",
				 "url":"https://app.eu.launchdarkly.com/shop/production/features/new-checkout"},
				{"time":"2026-05-04T09:30:00Z","flag":"dark-mode","name":"Dark mode","action":"updated the flag",
				 "actor":"access token deploy-bot","summary":"Changed the default rule to a percentage rollout",
				 "url":"https://app.eu.launchdarkly.com/shop/production/features/dark-mode"}
			]}`,
			wantEvidence: []source.Evidence{newCheckoutLink, darkModeLink},
		},
		{
			name:  "list_flag_changes for one flag says when more changes are left",
			tool:  flags.ToolListChanges,
			input: `{"environment":"staging","flag":"new-checkout","since":"2026-05-04T09:00:00Z","limit":3}`,
			path:  auditLogPath,
			response: []fakehttp.Response{body(fmt.Sprintf(`{"items":[
				{"_id":"a","date":%d,"kind":"flag","name":"New checkout","titleVerb":"turned off the flag","member":{"firstName":"Ada","lastName":"Lovelace"}},
				{"_id":"b","date":%d,"kind":"flag","name":"New checkout","titleVerb":"turned on the flag","app":{"name":"Terraform"}},
				{"_id":"c","date":%d,"kind":"flag","name":"New checkout","titleVerb":"updated the flag","subject":{"name":"SCIM"}},
				{"_id":"d","date":%d,"kind":"flag","name":"New checkout","titleVerb":"created the flag"}
			]}`, millis(9, 50), millis(9, 40), millis(9, 30), millis(9, 20)))},
			wantQueries: []string{oneFlagQuery},
			want: `{"project":"shop","environment":"staging","flag":"new-checkout","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","changes":[
				{"time":"2026-05-04T09:50:00Z","name":"New checkout","action":"turned off the flag","actor":"Ada Lovelace"},
				{"time":"2026-05-04T09:40:00Z","name":"New checkout","action":"turned on the flag","actor":"integration Terraform"},
				{"time":"2026-05-04T09:30:00Z","name":"New checkout","action":"updated the flag","actor":"SCIM"}
			]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "LaunchDarkly flag new-checkout in shop/staging",
				URL: appURL + "/shop/staging/features/new-checkout"}},
			wantTruncated: true,
		},
		{
			name:  "entries outside the range are left out",
			tool:  flags.ToolListChanges,
			input: `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			path:  auditLogPath,
			response: []fakehttp.Response{body(fmt.Sprintf(`{"items":[
				{"_id":"late","date":%d,"name":"x"},
				{"_id":"early","date":%d,"name":"y"}
			]}`, millis(10, 0), millis(8, 59)))},
			wantQueries:  []string{everyFlagQuery},
			wantNoData:   true,
			wantText:     "No flag changes in LaunchDarkly project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{productionLink},
		},
		{
			name:         "no changes is no data with a link to the flags",
			tool:         flags.ToolListChanges,
			input:        `{"environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			path:         auditLogPath,
			response:     []fakehttp.Response{body(`{"items":[],"_links":{}}`)},
			wantQueries:  []string{everyFlagQuery},
			wantNoData:   true,
			wantText:     "No flag changes in LaunchDarkly project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{productionLink},
		},
		{
			name:          "a request LaunchDarkly refuses is a tool error",
			tool:          flags.ToolListChanges,
			input:         `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			path:          auditLogPath,
			response:      []fakehttp.Response{failed(http.StatusBadRequest, "invalid_request", "invalid spec")},
			wantToolError: "LaunchDarkly answered 400 invalid_request: invalid spec",
		},
		{
			name:          "a resource the token's role may not read is a tool error",
			tool:          flags.ToolGetFlag,
			input:         `{"environment":"production","flag":"new-checkout"}`,
			path:          flagPath,
			response:      []fakehttp.Response{failed(http.StatusForbidden, "forbidden", "Access to the requested resource was denied")},
			wantToolError: "LaunchDarkly answered 403 forbidden: Access to the requested resource was denied",
		},
		{
			name:     "a refused token is a failure of the source",
			tool:     flags.ToolListChanges,
			input:    `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			path:     auditLogPath,
			response: []fakehttp.Response{failed(http.StatusUnauthorized, "unauthorized", "Invalid access token")},
			wantErr:  "LaunchDarkly answered 401 unauthorized: Invalid access token",
		},
		{
			name:     "a rate limit is a failure of the source",
			tool:     flags.ToolGetFlag,
			input:    `{"environment":"production","flag":"new-checkout"}`,
			path:     flagPath,
			response: []fakehttp.Response{{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "10"}}},
			wantErr:  "LaunchDarkly answered 429",
		},
		{
			name:        "get_flag reads one environment",
			tool:        flags.ToolGetFlag,
			input:       `{"project":"shop","environment":"production","flag":"new-checkout"}`,
			path:        flagPath,
			response:    []fakehttp.Response{fixture("flag.json")},
			wantQueries: []string{"env=production"},
			want: `{"project":"shop","environment":"production","flag":"new-checkout","name":"New checkout","kind":"boolean",
				"on":true,"rules":2,"targets":3,
				"fallthrough":"rollout by user: 25% variation 0: true (\"On\"), 75% variation 1: false (\"Off\")",
				"offVariation":"variation 1: false (\"Off\")","lastChanged":"2026-05-04T09:58:00Z","version":12,
				"url":"https://app.eu.launchdarkly.com/shop/production/features/new-checkout"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "LaunchDarkly flag new-checkout in shop/production",
				URL: appURL + "/shop/production/features/new-checkout", At: testNow.Add(-2 * time.Minute)}},
		},
		{
			name:  "get_flag of a flag that is off with one variation by default",
			tool:  flags.ToolGetFlag,
			input: `{"environment":"staging","flag":"new-checkout"}`,
			path:  flagPath,
			response: []fakehttp.Response{body(`{"key":"new-checkout","name":"New checkout","kind":"multivariate","archived":true,
				"variations":[{"value":{"color":"blue"}},{"value":"red","name":"Red"}],
				"environments":{"staging":{"on":false,"version":3,"fallthrough":{"variation":0},
				"_site":{"href":"//elsewhere.example.com/x"}}}}`)},
			wantQueries: []string{"env=staging"},
			want: `{"project":"shop","environment":"staging","flag":"new-checkout","name":"New checkout","kind":"multivariate",
				"on":false,"archived":true,"rules":0,"fallthrough":"variation 0: {\"color\":\"blue\"}","version":3,
				"url":"https://app.eu.launchdarkly.com/shop/staging/features/new-checkout"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "LaunchDarkly flag new-checkout in shop/staging",
				URL: appURL + "/shop/staging/features/new-checkout"}},
		},
		{
			name:       "a flag LaunchDarkly does not have is no data",
			tool:       flags.ToolGetFlag,
			input:      `{"environment":"production","flag":"new-checkout"}`,
			path:       flagPath,
			response:   []fakehttp.Response{failed(http.StatusNotFound, "not_found", "Unknown resource")},
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:       "a flag without the environment is no data",
			tool:       flags.ToolGetFlag,
			input:      `{"environment":"production","flag":"new-checkout"}`,
			path:       flagPath,
			response:   []fakehttp.Response{body(`{"key":"new-checkout","environments":{}}`)},
			wantNoData: true,
			wantText:   "Flag new-checkout of LaunchDarkly project shop has no configuration in environment production.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, tt.path, tt.response...)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			for i, req := range api.Requests() {
				require.Equal(t, testToken, req.Header.Get("Authorization"), "the token goes as it is, without a scheme")
				require.Equal(t, apiVersion, req.Header.Get("LD-API-Version"))
				require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "sidekick/"))

				if i < len(tt.wantQueries) {
					require.Equal(t, tt.wantQueries[i], req.Query.Encode(), "query of request %d", i)
				}
			}

			switch {
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
				require.NotContains(t, err.Error(), testToken)

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
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "a project outside the allowlist", tool: flags.ToolListChanges,
			input: `{"project":"billing","environment":"production","since":"2026-05-04T09:00:00Z"}`,
			want:  `project "billing" is not one Sidekick may read; allowed: shop`},
		{name: "an environment outside the allowlist", tool: flags.ToolGetFlag, input: `{"environment":"test","flag":"new-checkout"}`,
			want: `environment "test" of project shop is not one Sidekick may read; allowed: production, staging`},
		{name: "no environment when the project has two", tool: flags.ToolGetFlag, input: `{"flag":"new-checkout"}`,
			want: "give environment; allowed in project shop: production, staging"},
		{name: "a wildcard flag", tool: flags.ToolListChanges, input: `{"environment":"production","flag":"*","since":"2026-05-04T09:00:00Z"}`,
			want: `flag: "*" is not a valid key`},
		{name: "a flag that widens the resource specifier", tool: flags.ToolListChanges,
			input: `{"environment":"production","flag":"x;tag:env/*","since":"2026-05-04T09:00:00Z"}`, want: "is not a valid key"},
		{name: "a flag that climbs out of the path", tool: flags.ToolGetFlag, input: `{"environment":"production","flag":"../../members"}`,
			want: "is not a valid key"},
		{name: "no flag", tool: flags.ToolGetFlag, input: `{"environment":"production"}`, want: `flag: "" is not a valid key`},
		{name: "no since", tool: flags.ToolListChanges, input: `{"environment":"production"}`, want: "give since"},
		{name: "a range longer than a week", tool: flags.ToolListChanges, input: `{"environment":"production","since":"2026-04-01T00:00:00Z"}`,
			want: "longer than the 168h0m0s one call may read"},
		{name: "input that does not decode", tool: flags.ToolListChanges, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches LaunchDarkly")
		})
	}
}

func TestSourceListFlagChangesPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		// pages are the entries of each page LaunchDarkly answers with, as
		// minutes after 09:00 of the test day.
		pages [][]int
		// wantLimits are the limits of the requests, in order.
		wantLimits []string
		// wantBefores are the before times of the requests after the first.
		wantBefores   []int64
		wantChanges   int
		wantTruncated bool
	}{
		{
			name:  "a second page starts at the oldest entry of the first",
			limit: 25,
			// The second page holds the oldest entry of the first again.
			pages:         [][]int{minutes(59, 20), append([]int{40}, minutes(39, 3)...)},
			wantLimits:    []string{"20", "7"},
			wantBefores:   []int64{millis(9, 40) + 1},
			wantChanges:   23,
			wantTruncated: false,
		},
		{
			name:          "pages stop at the limit and say more are left",
			limit:         22,
			pages:         [][]int{minutes(59, 20), append([]int{40}, minutes(39, 3)...)},
			wantLimits:    []string{"20", "4"},
			wantBefores:   []int64{millis(9, 40) + 1},
			wantChanges:   22,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			responses := make([]fakehttp.Response, 0, len(tt.pages))
			for _, page := range tt.pages {
				responses = append(responses, body(auditItems(page)))
			}

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, auditLogPath, responses...)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			input := fmt.Sprintf(`{"environment":"production","since":"2026-05-04T09:00:00Z","limit":%d}`, tt.limit)
			res, err := s.Call(t.Context(), source.Call{Tool: flags.ToolListChanges, Input: json.RawMessage(input)})
			require.NoError(t, err)
			require.Equal(t, tt.wantTruncated, res.Truncated)

			requests := api.Requests()
			require.Len(t, requests, len(tt.wantLimits))

			for i, req := range requests {
				require.Equal(t, tt.wantLimits[i], req.Query.Get("limit"), "limit of request %d", i)

				if i > 0 {
					require.Equal(t, fmt.Sprint(tt.wantBefores[i-1]), req.Query.Get("before"), "before of request %d", i)
				}
			}

			var got flags.Changes
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Changes, tt.wantChanges)

			for i := 1; i < len(got.Changes); i++ {
				require.False(t, got.Changes[i].Time.After(got.Changes[i-1].Time), "newest first")
			}
		})
	}
}

func TestSourceCallCutsLongText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary int
		comment int
	}{
		{name: "a long summary is cut", summary: flags.MaxTextLen * 2},
		{name: "a long comment is cut", comment: flags.MaxTextLen + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entry := map[string]any{
				"_id": "a", "date": millis(9, 30), "kind": "flag", "name": "New checkout",
				"description": strings.Repeat("é", tt.summary/2), "comment": strings.Repeat("x", tt.comment),
				"accesses": []map[string]string{{"action": "updateOn", "resource": "proj/shop:env/production:flag/new-checkout"}},
			}

			data, err := json.Marshal(map[string]any{"items": []any{entry}})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, auditLogPath, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: flags.ToolListChanges,
				Input: json.RawMessage(`{"environment":"production","since":"2026-05-04T09:00:00Z"}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)

			var got flags.Changes
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Changes, 1)
			require.LessOrEqual(t, len(got.Changes[0].Summary), flags.MaxTextLen)
			require.LessOrEqual(t, len(got.Changes[0].Comment), flags.MaxTextLen)
		})
	}
}

func TestSourceTokenFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an empty token secret", secret: []byte(" \n"), wantErr: "the token secret is empty"},
		{name: "a token secret that cannot be read", err: errors.New("no such file"), wantErr: "no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return(tt.secret, tt.err)

			api := fakehttp.New(t)
			s := newTestSource(t, api, secrets)

			_, err := s.Call(t.Context(), source.Call{Tool: flags.ToolGetFlag, Input: json.RawMessage(`{"environment":"production","flag":"new-checkout"}`)})
			require.ErrorContains(t, err, tt.wantErr)

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
		{name: "a tool that writes", tool: "update_flag"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cfg := Config{TokenSecret: testTokenRef, Projects: testProjects}

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
		wantBaseURL string
	}{
		{name: "without a secrets resolver", cfg: cfg, wantErr: errNoSecrets.Error()},
		{name: "with settings that do not check", cfg: Config{TokenSecret: testTokenRef, Projects: testProjects,
			Settings: source.Settings{Concurrency: -1}}, withSecrets: true, wantErr: "concurrency: must not be negative"},
		{name: "with the default base URL and HTTP client", cfg: cfg, withSecrets: true, wantBaseURL: DefaultBaseURL},
		{name: "with the Federal instance", cfg: Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: "https://app.launchdarkly.us/"},
			withSecrets: true, wantBaseURL: "https://app.launchdarkly.us"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			built, err := New(t.Context(), "launchdarkly", tt.cfg, deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			s, ok := built.(*Source)
			require.True(t, ok)
			require.Equal(t, tt.wantBaseURL, s.client.baseURL)

			tools, err := s.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, s.Close())
		})
	}
}

// minutes returns n minutes after 09:00, counting down from first.
func minutes(first, n int) []int {
	out := make([]int, 0, n)
	for i := range n {
		out = append(out, first-i)
	}

	return out
}

// auditItems returns an audit log page with one flag entry at each of the
// minutes after 09:00 of the test day, with the minute as its ID.
func auditItems(mins []int) string {
	items := make([]string, 0, len(mins))
	for _, m := range mins {
		items = append(items, fmt.Sprintf(`{"_id":"e%d","date":%d,"kind":"flag","name":"Flag %d","titleVerb":"updated the flag",
			"accesses":[{"action":"updateRules","resource":"proj/shop:env/production:flag/flag-%d"}]}`, m, millis(9, m), m, m))
	}

	return `{"items":[` + strings.Join(items, ",") + `]}`
}

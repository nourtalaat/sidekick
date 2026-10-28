package unleash

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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

// featureAPIPath is the path of the fake API's flag.
const featureAPIPath = "/api/admin/projects/shop/features/new-checkout"

// Queries of the event search requests, for the last hour of the test day.
// Unleash filters by whole days, so they ask for the day before and the day
// after.
const (
	// everyFlagQuery reads every flag of shop/production, 20 events and one
	// more to know whether there are others.
	everyFlagQuery = "environment=IS%3Aproduction&from=IS%3A2026-05-03&limit=21&offset=0&project=IS%3Ashop&to=IS%3A2026-05-05"
	// oneFlagQuery reads new-checkout in shop/staging, 3 events and one more.
	oneFlagQuery = "environment=IS%3Astaging&feature=IS%3Anew-checkout&from=IS%3A2026-05-03&limit=4&offset=0&project=IS%3Ashop&to=IS%3A2026-05-05"
	// lastChangeQuery reads the newest event of new-checkout in
	// shop/production.
	lastChangeQuery = "environment=IS%3Aproduction&feature=IS%3Anew-checkout&limit=1&project=IS%3Ashop"
)

// Evidence links of the tests.
var (
	newCheckoutLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "Unleash flag new-checkout in shop/production",
		URL: appURL + "/projects/shop/features/new-checkout"}
	darkModeLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "Unleash flag dark-mode in shop/production",
		URL: appURL + "/projects/shop/features/dark-mode"}
	projectLink = source.Evidence{Kind: source.EvidenceDeploy, Title: "Unleash flags in shop/production",
		URL: appURL + "/projects/shop"}
)

// route is the answers of the fake API to the requests for one path.
type route struct {
	path      string
	responses []fakehttp.Response
}

func TestSourceCall(t *testing.T) {
	t.Parallel()

	unauthorized := fakehttp.Response{
		Status:  http.StatusUnauthorized,
		Headers: jsonHeaders,
		Body: []byte(`{"path":"/api/admin/search/events","type":"password","message":"You must log in to use Unleash. ` +
			`Your request had no authorization header, so we could not authorize you."}`),
	}

	tests := []struct {
		name   string
		tool   string
		input  string
		routes []route
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
			routes:      []route{{searchEventsPath, []fakehttp.Response{fixture("events.json")}}},
			wantQueries: []string{everyFlagQuery},
			want: `{"project":"shop","environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","changes":[
				{"time":"2026-05-04T09:58:00Z","flag":"new-checkout","action":"feature-environment-enabled","actor":"ada@example.com",
				 "summary":"**ada@example.com** enabled **[new-checkout](https://unleash.example.com/projects/shop/features/new-checkout)** in *production*",
				 "url":"https://unleash.example.com/projects/shop/features/new-checkout"},
				{"time":"2026-05-04T09:30:00Z","flag":"dark-mode","action":"feature-strategy-update","actor":"deploy-bot",
				 "summary":"**deploy-bot** updated the strategy *flexibleRollout* of **dark-mode** in *production*",
				 "url":"https://unleash.example.com/projects/shop/features/dark-mode"}
			]}`,
			wantEvidence: []source.Evidence{newCheckoutLink, darkModeLink},
		},
		{
			name:  "list_flag_changes for one flag says when more changes are left",
			tool:  flags.ToolListChanges,
			input: `{"environment":"staging","flag":"new-checkout","since":"2026-05-04T09:00:00Z","limit":3}`,
			routes: []route{{searchEventsPath, []fakehttp.Response{body(`{"total":9,"events":[
				{"id":4,"createdAt":"2026-05-04T09:50:00Z","type":"feature-environment-disabled","createdBy":"ada@example.com","featureName":"new-checkout","label":"Flag disabled"},
				{"id":3,"createdAt":"2026-05-04T09:40:00Z","type":"feature-environment-enabled","createdBy":"terraform","featureName":"new-checkout","summary":"enabled"},
				{"id":2,"createdAt":"2026-05-04T09:30:00Z","type":"feature-strategy-add","createdBy":"deploy-bot","featureName":"new-checkout"},
				{"id":1,"createdAt":"2026-05-04T09:20:00Z","type":"feature-strategy-remove","createdBy":"deploy-bot","featureName":"new-checkout"}
			]}`)}}},
			wantQueries: []string{oneFlagQuery},
			want: `{"project":"shop","environment":"staging","flag":"new-checkout","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z","changes":[
				{"time":"2026-05-04T09:50:00Z","flag":"new-checkout","action":"feature-environment-disabled","actor":"ada@example.com","summary":"Flag disabled",
				 "url":"https://unleash.example.com/projects/shop/features/new-checkout"},
				{"time":"2026-05-04T09:40:00Z","flag":"new-checkout","action":"feature-environment-enabled","actor":"terraform","summary":"enabled",
				 "url":"https://unleash.example.com/projects/shop/features/new-checkout"},
				{"time":"2026-05-04T09:30:00Z","flag":"new-checkout","action":"feature-strategy-add","actor":"deploy-bot",
				 "url":"https://unleash.example.com/projects/shop/features/new-checkout"}
			]}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Unleash flag new-checkout in shop/staging",
				URL: appURL + "/projects/shop/features/new-checkout"}},
			wantTruncated: true,
		},
		{
			name:  "events outside the range and of other scopes are left out",
			tool:  flags.ToolListChanges,
			input: `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			routes: []route{{searchEventsPath, []fakehttp.Response{body(`{"total":4,"events":[
				{"id":4,"createdAt":"2026-05-04T10:00:00Z","type":"feature-environment-enabled","createdBy":"a","featureName":"late"},
				{"id":3,"createdAt":"2026-05-04T09:50:00Z","type":"feature-environment-enabled","createdBy":"a","featureName":"other-env","environment":"staging"},
				{"id":2,"createdAt":"2026-05-04T09:40:00Z","type":"feature-environment-enabled","createdBy":"a","featureName":"other-project","project":"billing"},
				{"id":1,"createdAt":"2026-05-04T08:59:59Z","type":"feature-environment-enabled","createdBy":"a","featureName":"early"}
			]}`)}}},
			wantQueries:  []string{everyFlagQuery},
			wantNoData:   true,
			wantText:     "No flag changes in Unleash project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{projectLink},
		},
		{
			name:         "no changes is no data with a link to the project",
			tool:         flags.ToolListChanges,
			input:        `{"environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`,
			routes:       []route{{searchEventsPath, []fakehttp.Response{body(`{"events":[],"total":0}`)}}},
			wantQueries:  []string{everyFlagQuery},
			wantNoData:   true,
			wantText:     "No flag changes in Unleash project shop, environment production, between 2026-05-04T09:00:00Z and 2026-05-04T10:00:00Z.",
			wantEvidence: []source.Evidence{projectLink},
		},
		{
			name:       "a project Unleash does not have is no data",
			tool:       flags.ToolListChanges,
			input:      `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			routes:     []route{{searchEventsPath, []fakehttp.Response{failed(http.StatusNotFound, "NotFoundError", "No project found")}}},
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:          "a request Unleash refuses is a tool error",
			tool:          flags.ToolListChanges,
			input:         `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			routes:        []route{{searchEventsPath, []fakehttp.Response{failed(http.StatusBadRequest, "BadDataError", "Request validation failed")}}},
			wantToolError: "Unleash answered 400 BadDataError: Request validation failed",
		},
		{
			name:    "a refused token is a failure of the source",
			tool:    flags.ToolListChanges,
			input:   `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			routes:  []route{{searchEventsPath, []fakehttp.Response{unauthorized}}},
			wantErr: "Unleash answered 401: You must log in to use Unleash",
		},
		{
			name:    "a token that may not read is a failure of the source",
			tool:    flags.ToolGetFlag,
			input:   `{"environment":"production","flag":"new-checkout"}`,
			routes:  []route{{featureAPIPath, []fakehttp.Response{failed(http.StatusForbidden, "NoAccessError", "You don't have the required permissions")}}},
			wantErr: "Unleash answered 403 NoAccessError: You don't have the required permissions",
		},
		{
			name:    "a rate limit is a failure of the source",
			tool:    flags.ToolGetFlag,
			input:   `{"environment":"production","flag":"new-checkout"}`,
			routes:  []route{{featureAPIPath, []fakehttp.Response{{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "10"}}}}},
			wantErr: "Unleash answered 429",
		},
		{
			name:    "a server error is a failure of the source",
			tool:    flags.ToolListChanges,
			input:   `{"environment":"production","since":"2026-05-04T09:00:00Z"}`,
			routes:  []route{{searchEventsPath, []fakehttp.Response{{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")}}}},
			wantErr: "Unleash answered 502",
		},
		{
			name:  "get_flag reads one environment",
			tool:  flags.ToolGetFlag,
			input: `{"project":"shop","environment":"production","flag":"new-checkout"}`,
			routes: []route{
				{featureAPIPath, []fakehttp.Response{fixture("feature.json")}},
				{searchEventsPath, []fakehttp.Response{fixture("events.json")}},
			},
			wantQueries: []string{"", lastChangeQuery},
			want: `{"project":"shop","environment":"production","flag":"new-checkout","kind":"release","on":true,"rules":2,"prerequisites":1,
				"lastChanged":"2026-05-04T09:58:00Z","url":"https://unleash.example.com/projects/shop/features/new-checkout"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Unleash flag new-checkout in shop/production",
				URL: appURL + "/projects/shop/features/new-checkout", At: testNow.Add(-2 * time.Minute)}},
		},
		{
			name:  "get_flag of a flag that is off, without events",
			tool:  flags.ToolGetFlag,
			input: `{"environment":"staging","flag":"new-checkout"}`,
			routes: []route{
				{featureAPIPath, []fakehttp.Response{body(`{"name":"new-checkout","type":"kill-switch","environments":[{"name":"staging","enabled":false,"strategies":[]}]}`)}},
				{searchEventsPath, []fakehttp.Response{body(`{"events":[],"total":0}`)}},
			},
			wantQueries: []string{"", "environment=IS%3Astaging&feature=IS%3Anew-checkout&limit=1&project=IS%3Ashop"},
			want: `{"project":"shop","environment":"staging","flag":"new-checkout","kind":"kill-switch","on":false,"rules":0,
				"url":"https://unleash.example.com/projects/shop/features/new-checkout"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Unleash flag new-checkout in shop/staging",
				URL: appURL + "/projects/shop/features/new-checkout"}},
		},
		{
			name:  "get_flag without the right to read events still gives the state",
			tool:  flags.ToolGetFlag,
			input: `{"environment":"production","flag":"new-checkout"}`,
			routes: []route{
				{featureAPIPath, []fakehttp.Response{fixture("feature.json")}},
				{searchEventsPath, []fakehttp.Response{failed(http.StatusForbidden, "NoAccessError", "You don't have the required permissions")}},
			},
			wantQueries: []string{"", lastChangeQuery},
			want: `{"project":"shop","environment":"production","flag":"new-checkout","kind":"release","on":true,"rules":2,"prerequisites":1,
				"url":"https://unleash.example.com/projects/shop/features/new-checkout"}`,
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "Unleash flag new-checkout in shop/production",
				URL: appURL + "/projects/shop/features/new-checkout"}},
		},
		{
			name:  "get_flag with an event search that fails is a failure of the source",
			tool:  flags.ToolGetFlag,
			input: `{"environment":"production","flag":"new-checkout"}`,
			routes: []route{
				{featureAPIPath, []fakehttp.Response{fixture("feature.json")}},
				{searchEventsPath, []fakehttp.Response{{Status: http.StatusInternalServerError}}},
			},
			wantErr: "Unleash answered 500",
		},
		{
			name:       "a flag Unleash does not have is no data",
			tool:       flags.ToolGetFlag,
			input:      `{"environment":"production","flag":"new-checkout"}`,
			routes:     []route{{featureAPIPath, []fakehttp.Response{failed(http.StatusNotFound, "NotFoundError", "Could not find feature")}}},
			wantNoData: true,
			wantText:   notFoundText,
		},
		{
			name:       "a flag without the environment is no data",
			tool:       flags.ToolGetFlag,
			input:      `{"environment":"staging","flag":"new-checkout"}`,
			routes:     []route{{featureAPIPath, []fakehttp.Response{fixture("feature.json")}}},
			wantNoData: true,
			wantText:   "Flag new-checkout of Unleash project shop has no configuration in environment staging.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			for _, r := range tt.routes {
				api.Handle(http.MethodGet, r.path, r.responses...)
			}

			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			for i, req := range api.Requests() {
				require.Equal(t, testToken, req.Header.Get("Authorization"), "the token goes as it is, without a scheme")
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
		{name: "a flag that adds a filter value", tool: flags.ToolListChanges,
			input: `{"environment":"production","flag":"x,y","since":"2026-05-04T09:00:00Z"}`, want: `flag: "x,y" is not a valid key`},
		{name: "a flag with an operator", tool: flags.ToolListChanges,
			input: `{"environment":"production","flag":"IS_NOT:x","since":"2026-05-04T09:00:00Z"}`, want: "is not a valid key"},
		{name: "a flag that climbs out of the path", tool: flags.ToolGetFlag, input: `{"environment":"production","flag":"../../users"}`,
			want: "is not a valid key"},
		{name: "a flag that is a dot segment", tool: flags.ToolGetFlag, input: `{"environment":"production","flag":".."}`,
			want: "is not a valid key"},
		{name: "a flag with an escaped slash", tool: flags.ToolGetFlag, input: `{"environment":"production","flag":"a%2Fb"}`,
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
			require.Empty(t, api.Requests(), "the call never reaches Unleash")
		})
	}
}

func TestSourceListFlagChangesPages(t *testing.T) {
	t.Parallel()

	first := at(9, 59, 59)
	page := every(first, 5*time.Second, eventPageSize)
	// next are the events after the first page, older than all of it.
	next := every(first.Add(-eventPageSize*5*time.Second), 5*time.Second, 2)
	before := at(8, 0, 0)

	tests := []struct {
		name string
		// pages are the answers of Unleash, in order.
		pages []string
		// wantLimits and wantOffsets are those of the requests, in order.
		wantLimits    []string
		wantOffsets   []string
		wantChanges   int
		wantTruncated bool
	}{
		{
			name:        "a full page that holds every event ends the read",
			pages:       []string{eventsPage(eventPageSize, page...)},
			wantLimits:  []string{"100"},
			wantOffsets: []string{"0"},
			wantChanges: 100,
		},
		{
			name:          "a second page, asked for one more event, says more are left",
			pages:         []string{eventsPage(130, page...), eventsPage(130, next[0])},
			wantLimits:    []string{"100", "1"},
			wantOffsets:   []string{"0", "100"},
			wantChanges:   100,
			wantTruncated: true,
		},
		{
			name:        "a second page that reaches events before the range ends the read",
			pages:       []string{eventsPage(130, page...), eventsPage(130, before)},
			wantLimits:  []string{"100", "1"},
			wantOffsets: []string{"0", "100"},
			wantChanges: 100,
		},
		{
			name:          "an event that moved to the next page is skipped, not read twice",
			pages:         []string{eventsPage(102, page...), eventsPage(102, page[len(page)-1]), eventsPage(102, next[0])},
			wantLimits:    []string{"100", "1", "1"},
			wantOffsets:   []string{"0", "100", "101"},
			wantChanges:   100,
			wantTruncated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			responses := make([]fakehttp.Response, 0, len(tt.pages))
			for _, p := range tt.pages {
				responses = append(responses, body(p))
			}

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, searchEventsPath, responses...)
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			input := fmt.Sprintf(`{"environment":"production","since":"2026-05-04T09:00:00Z","limit":%d}`, flags.MaxChanges)
			res, err := s.Call(t.Context(), source.Call{Tool: flags.ToolListChanges, Input: json.RawMessage(input)})
			require.NoError(t, err)
			require.Equal(t, tt.wantTruncated, res.Truncated)

			requests := api.Requests()
			require.Len(t, requests, len(tt.wantLimits))

			for i, req := range requests {
				require.Equal(t, tt.wantLimits[i], req.Query.Get("limit"), "limit of request %d", i)
				require.Equal(t, tt.wantOffsets[i], req.Query.Get("offset"), "offset of request %d", i)
			}

			var got flags.Changes
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Changes, tt.wantChanges)

			for i := 1; i < len(got.Changes); i++ {
				require.True(t, got.Changes[i].Time.Before(got.Changes[i-1].Time), "newest first, each event once")
			}
		})
	}
}

func TestSourceListFlagChangesStopsPaging(t *testing.T) {
	t.Parallel()

	// Every page is full of events after the range, so nothing in it is a
	// change, and the read stops at its page bound instead of going on.
	late := every(at(10, 30, 0), time.Second, 21)

	responses := make([]fakehttp.Response, 0, maxEventPages)
	for range maxEventPages {
		responses = append(responses, body(eventsPage(1000, late...)))
	}

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, searchEventsPath, responses...)
	api.ExpectAllCalled()

	s := newTestSource(t, api, tokenSecrets(t))

	res, err := s.Call(t.Context(), source.Call{Tool: flags.ToolListChanges,
		Input: json.RawMessage(`{"environment":"production","since":"2026-05-04T09:00:00Z","until":"2026-05-04T10:00:00Z"}`)})
	require.NoError(t, err)
	require.True(t, res.NoData)
	require.Len(t, api.Requests(), maxEventPages)

	for i, req := range api.Requests() {
		require.Equal(t, strconv.Itoa(i*len(late)), req.Query.Get("offset"))
	}
}

func TestSourceCallCutsLongText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary int
		label   int
	}{
		{name: "a long summary is cut", summary: flags.MaxTextLen * 2},
		{name: "a long label used as the summary is cut", label: flags.MaxTextLen + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := map[string]any{
				"id": 1, "createdAt": "2026-05-04T09:30:00Z", "type": "feature-strategy-update", "createdBy": "ada@example.com",
				"featureName": "new-checkout", "summary": strings.Repeat("é", tt.summary/2), "label": strings.Repeat("x", tt.label),
			}

			data, err := json.Marshal(map[string]any{"events": []any{e}, "total": 1})
			require.NoError(t, err)

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, searchEventsPath, fakehttp.Response{Headers: jsonHeaders, Body: data})

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: flags.ToolListChanges,
				Input: json.RawMessage(`{"environment":"production","since":"2026-05-04T09:00:00Z"}`)})
			require.NoError(t, err)
			require.True(t, res.Truncated)

			var got flags.Changes
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Changes, 1)
			require.NotEmpty(t, got.Changes[0].Summary)
			require.LessOrEqual(t, len(got.Changes[0].Summary), flags.MaxTextLen)
		})
	}
}

func TestSourceOwnLinks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "a link on the instance stays", text: "[x](https://unleash.example.com/projects/shop)", want: "[x](https://unleash.example.com/projects/shop)"},
		{name: "a link on the host in other case stays", text: "[x](https://Unleash.Example.com/p)", want: "[x](https://Unleash.Example.com/p)"},
		{name: "a link to another host is its text", text: "see [the flag](https://elsewhere.example.com/x) now", want: "see the flag now"},
		{name: "a link to a host that starts like the instance", text: "[x](https://unleash.example.com.evil.example/p)", want: "x"},
		{name: "a link with plain http is its text", text: "[x](http://unleash.example.com/p)", want: "x"},
		{name: "a link without a host is its text", text: "[x](/projects/shop)", want: "x"},
		{name: "a script link is its text", text: "[x](javascript:void)", want: "x"},
		{name: "an image is its text", text: "![chart](https://elsewhere.example.com/c.png)", want: "chart"},
		{name: "text without links stays", text: "**ada** enabled *flag*", want: "**ada** enabled *flag*"},
		{name: "two links, one kept", text: "[a](https://unleash.example.com/a) [b](https://elsewhere.example.com/b)", want: "[a](https://unleash.example.com/a) b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), registry.NewMockSecrets(gomock.NewController(t)))

			require.Equal(t, tt.want, s.ownLinks(tt.text))
		})
	}
}

func TestSourceFlagURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		project string
		flag    string
		want    string
	}{
		{name: "a flag", baseURL: "https://unleash.example.com", project: "default", flag: "new-checkout",
			want: "https://unleash.example.com/projects/default/features/new-checkout"},
		{name: "a project", baseURL: "https://unleash.example.com/", project: "default",
			want: "https://unleash.example.com/projects/default"},
		{name: "an instance under a path", baseURL: "https://example.com/unleash/", project: "shop", flag: "a.b_c-d",
			want: "https://example.com/unleash/projects/shop/features/a.b_c-d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Source{cfg: Config{BaseURL: tt.baseURL}}

			require.Equal(t, tt.want, s.flagURL(tt.project, tt.flag))
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

	cfg := Config{BaseURL: appURL, TokenSecret: testTokenRef, Projects: testProjects}

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
		wantBaseURL string
	}{
		{name: "without a secrets resolver", cfg: cfg, wantErr: errNoSecrets.Error()},
		{name: "with settings that do not check", cfg: Config{BaseURL: appURL, TokenSecret: testTokenRef, Projects: testProjects,
			Settings: source.Settings{Concurrency: -1}}, withSecrets: true, wantErr: "concurrency: must not be negative"},
		{name: "with the default HTTP client", cfg: cfg, withSecrets: true, wantBaseURL: appURL},
		{name: "with a base URL under a path", cfg: Config{BaseURL: "https://example.com/unleash/", TokenSecret: testTokenRef, Projects: testProjects},
			withSecrets: true, wantBaseURL: "https://example.com/unleash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			built, err := New(t.Context(), "unleash", tt.cfg, deps)
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

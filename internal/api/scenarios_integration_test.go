//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/scenario"
	"github.com/nourtalaat/sidekick/internal/scrub"
)

// The investigations in fixture/scenarios.sql.
const (
	scnInv         = "0198f2a4-0000-7000-8000-0000000000c1"
	scnSlackInv    = "0198f2a4-0000-7000-8000-0000000000c2"
	scnUnconfirmed = "0198f2a4-0000-7000-8000-0000000000c3"
	scnMissing     = "0198f2a4-0000-7000-8000-0000000000ff"
	scnAdminToken  = "skt_admin"
)

// scenarioBody is a Scenario of the API.
type scenarioBody struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	Revision        int32   `json:"revision"`
	InvestigationID string  `json:"investigationId"`
	ReviewedBy      *string `json:"reviewedBy"`
	ReviewedAt      *string `json:"reviewedAt"`
}

// scenarioDetailBody is a ScenarioDetail of the API.
type scenarioDetailBody struct {
	Scenario scenarioBody `json:"scenario"`
	Body     string       `json:"body"`
	Notes    []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"notes"`
	Pairs []struct {
		Ref      string  `json:"ref"`
		Original *string `json:"original"`
		Cleaned  string  `json:"cleaned"`
	} `json:"pairs"`
}

// problemBody is the problem details of an error answer.
type problemBody struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

// setupScenarios is a server with the scenario service over the fixture's
// investigation, which was exported by the alertmanager endpoint.
func setupScenarios(t *testing.T) server {
	t.Helper()

	s := setupWith(t, nil, "fixture/scenarios.sql")

	cleaner, err := scrub.New(scrub.Options{HashKey: []byte("test-key")})
	require.NoError(t, err)

	service := scenario.NewService(scenario.Options{
		Pool: s.pool, Cleaner: cleaner, Audit: audit.NewWriter(),
		Webhooks: map[string]config.Webhook{"alertmanager": {Section: config.Section{Type: "alertmanager"}}},
		Sources:  map[string]config.Section{"signoz": {Type: "signoz"}, "k8s": {Type: "kubernetes"}},
	})

	return s.replica(t, func(o *api.Options) { o.Scenarios = service })
}

// adminDo sends a request as the admin with a session and the CSRF token.
func adminDo(t *testing.T, s server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	return s.do(t, method, path, adSession, adCSRF, strings.NewReader(body))
}

// exportScenario exports the fixture's investigation and returns the draft.
func exportScenario(t *testing.T, s server) scenarioBody {
	t.Helper()

	rec := adminDo(t, s, http.MethodPost, "/api/v1/investigations/"+scnInv+"/scenario-draft", "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var out scenarioBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

// getScenario reads a scenario with its review view.
func getScenario(t *testing.T, s server, id string) scenarioDetailBody {
	t.Helper()

	rec := adminDo(t, s, http.MethodGet, "/api/v1/scenarios/"+id, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var out scenarioDetailBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out
}

// problemOf reads the problem details of an answer.
func problemOf(t *testing.T, rec *httptest.ResponseRecorder) problemBody {
	t.Helper()

	var p problemBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())

	return p
}

func TestScenarioDraft(t *testing.T) {
	t.Parallel()

	s := setupScenarios(t)

	draft := exportScenario(t, s)
	require.Equal(t, "draft", draft.Status)
	require.EqualValues(t, 1, draft.Revision)
	require.Equal(t, scnInv, draft.InvestigationID)
	require.Equal(t, "checkout-api latency above 2s", draft.Title)

	// Asking again answers with the same scenario.
	again := adminDo(t, s, http.MethodPost, "/api/v1/investigations/"+scnInv+"/scenario-draft", "")
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	require.Contains(t, again.Body.String(), draft.ID)

	// The review view has the cleaned file next to the originals.
	detail := getScenario(t, s, draft.ID)
	require.Contains(t, detail.Body, "host-1")
	require.NotContains(t, detail.Body, "acme.com")
	require.NotContains(t, detail.Body, "10.1.2.3")
	require.NotContains(t, detail.Body, "alice")

	refs := map[string]string{}
	originals := map[string]string{}

	for _, p := range detail.Pairs {
		refs[p.Ref] = p.Cleaned

		if p.Original != nil {
			originals[p.Ref] = *p.Original
		}
	}

	require.Contains(t, originals["event.payload"], "web-7.prod.acme.com")
	require.Contains(t, originals["sources.signoz.fixtures[0].respond.text"], "db-1.prod.acme.com")
	require.Contains(t, refs["sources.signoz.fixtures[0].respond.text"], "(ip-1)")

	kinds := []string{}
	for _, n := range detail.Notes {
		kinds = append(kinds, n.Kind)
	}

	require.Contains(t, kinds, scenario.NoteUnavailable)
	require.Contains(t, kinds, scenario.NoteNoRubric)

	var actions []string

	rows, err := s.pool.Query(t.Context(), `SELECT action FROM audit_log WHERE target_id = $1 ORDER BY id`, draft.ID)
	require.NoError(t, err)

	defer rows.Close()

	for rows.Next() {
		var a string

		require.NoError(t, rows.Scan(&a))

		actions = append(actions, a)
	}

	require.Equal(t, []string{scenario.ActionDrafted}, actions)
}

func TestScenarioDraftRefusals(t *testing.T) {
	t.Parallel()

	s := setupScenarios(t)

	tests := []struct {
		name       string
		id         string
		session    string
		csrf       string
		wantStatus int
		wantCode   string
	}{
		{name: "a viewer", id: scnInv, session: viSession, csrf: viCSRF, wantStatus: http.StatusForbidden},
		{name: "no session", id: scnInv, wantStatus: http.StatusUnauthorized},
		{name: "no such investigation", id: scnMissing, session: adSession, csrf: adCSRF, wantStatus: http.StatusNotFound},
		{name: "not confirmed", id: scnUnconfirmed, session: adSession, csrf: adCSRF, wantStatus: http.StatusConflict, wantCode: "not_confirmed"},
		{name: "opened by a Slack message", id: scnSlackInv, session: adSession, csrf: adCSRF, wantStatus: http.StatusUnprocessableEntity, wantCode: "no_replay"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodPost, "/api/v1/investigations/"+tt.id+"/scenario-draft", tt.session, tt.csrf, nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantCode != "" {
				require.Equal(t, tt.wantCode, problemOf(t, rec).Code)
			}
		})
	}
}

func TestScenariosWithoutService(t *testing.T) {
	t.Parallel()

	s := setupWith(t, nil, "fixture/scenarios.sql")

	for _, tt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/investigations/" + scnInv + "/scenario-draft"},
		{http.MethodGet, "/api/v1/scenarios"},
		{http.MethodGet, "/api/v1/scenarios/some-scenario"},
		{http.MethodGet, "/api/v1/scenarios/some-scenario/file"},
	} {
		rec := adminDo(t, s, tt.method, tt.path, "")
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, tt.path)
	}
}

func TestScenarioReviewAndFile(t *testing.T) {
	t.Parallel()

	s := setupScenarios(t)
	draft := exportScenario(t, s)
	path := "/api/v1/scenarios/" + draft.ID

	// A draft cannot be downloaded, by a session or by an admin token.
	for _, rec := range []*httptest.ResponseRecorder{
		adminDo(t, s, http.MethodGet, path+"/file", ""),
		s.doWithToken(t, http.MethodGet, path+"/file", scnAdminToken, nil),
	} {
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "not_reviewed", problemOf(t, rec).Code)
	}

	review := func(t *testing.T, revision int32, checked bool) *httptest.ResponseRecorder {
		t.Helper()

		body, err := json.Marshal(map[string]any{"revision": revision, "sensitiveDataChecked": checked})
		require.NoError(t, err)

		return adminDo(t, s, http.MethodPost, path+"/review", string(body))
	}

	// Review needs the tick, the revision that was read, and a person.
	unticked := review(t, draft.Revision, false)
	require.Equal(t, http.StatusUnprocessableEntity, unticked.Code, unticked.Body.String())
	require.Equal(t, "sensitiveDataChecked", problemOf(t, unticked).Errors[0].Field)

	stale := review(t, draft.Revision+1, true)
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	require.Equal(t, "stale", problemOf(t, stale).Code)

	byToken := s.doWithToken(t, http.MethodPost, path+"/review", scnAdminToken, strings.NewReader(`{"revision":1,"sensitiveDataChecked":true}`))
	require.Equal(t, http.StatusForbidden, byToken.Code, byToken.Body.String())

	rec := review(t, draft.Revision, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var reviewed scenarioBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reviewed))
	require.Equal(t, "reviewed", reviewed.Status)
	require.NotNil(t, reviewed.ReviewedBy)
	require.Equal(t, "Ad", *reviewed.ReviewedBy)
	require.NotNil(t, reviewed.ReviewedAt)

	again := review(t, draft.Revision, true)
	require.Equal(t, http.StatusConflict, again.Code)
	require.Equal(t, "already_reviewed", problemOf(t, again).Code)

	// A reviewed scenario downloads as an attachment, and is a valid scenario.
	file := s.doWithToken(t, http.MethodGet, path+"/file", scnAdminToken, nil)
	require.Equal(t, http.StatusOK, file.Code, file.Body.String())
	require.Equal(t, "application/yaml", file.Header().Get("Content-Type"))
	require.Equal(t, "attachment; filename="+draft.ID+".yaml", file.Header().Get("Content-Disposition"))

	sc, err := bench.ParseScenario(file.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, draft.ID, sc.ID)

	var downloads int

	require.NoError(t, s.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2`, scenario.ActionDownloaded, draft.ID).Scan(&downloads))
	require.Equal(t, 1, downloads)

	// Editing it makes it a draft again, and it cannot be downloaded.
	edited, err := json.Marshal(map[string]any{
		"revision": reviewed.Revision,
		"body":     strings.Replace(file.Body.String(), "title: checkout-api latency above 2s", "title: Checkout latency", 1),
	})
	require.NoError(t, err)

	put := adminDo(t, s, http.MethodPut, path, string(edited))
	require.Equal(t, http.StatusOK, put.Code, put.Body.String())

	var updated scenarioBody

	require.NoError(t, json.Unmarshal(put.Body.Bytes(), &updated))
	require.Equal(t, "draft", updated.Status)
	require.Equal(t, "Checkout latency", updated.Title)
	require.Nil(t, updated.ReviewedBy)
	require.Equal(t, reviewed.Revision+1, updated.Revision)

	after := adminDo(t, s, http.MethodGet, path+"/file", "")
	require.Equal(t, http.StatusConflict, after.Code)
}

func TestScenarioUpdateRefusals(t *testing.T) {
	t.Parallel()

	s := setupScenarios(t)
	draft := exportScenario(t, s)
	detail := getScenario(t, s, draft.ID)
	path := "/api/v1/scenarios/" + draft.ID

	put := func(revision int32, body string) *httptest.ResponseRecorder {
		raw, err := json.Marshal(map[string]any{"revision": revision, "body": body})
		require.NoError(t, err)

		return adminDo(t, s, http.MethodPut, path, string(raw))
	}

	stale := put(draft.Revision+1, detail.Body)
	require.Equal(t, http.StatusConflict, stale.Code)
	require.Equal(t, "stale", problemOf(t, stale).Code)

	invalid := put(draft.Revision, "id: "+draft.ID+"\n")
	require.Equal(t, http.StatusUnprocessableEntity, invalid.Code, invalid.Body.String())

	p := problemOf(t, invalid)
	require.Equal(t, "validation", p.Code)
	require.NotEmpty(t, p.Errors)

	leaky := put(draft.Revision, strings.Replace(detail.Body, "title: checkout-api latency above 2s", "title: ask bob@acme.com", 1))
	require.Equal(t, http.StatusUnprocessableEntity, leaky.Code, leaky.Body.String())

	p = problemOf(t, leaky)
	require.Equal(t, "sensitive_data", p.Code)
	require.NotContains(t, leaky.Body.String(), "bob@")

	missing := adminDo(t, s, http.MethodGet, "/api/v1/scenarios/no-such-scenario", "")
	require.Equal(t, http.StatusNotFound, missing.Code)

	// Nothing changed.
	require.EqualValues(t, draft.Revision, getScenario(t, s, draft.ID).Scenario.Revision)
}

func TestScenarioList(t *testing.T) {
	t.Parallel()

	s := setupScenarios(t)
	draft := exportScenario(t, s)

	list := func(query string) (*httptest.ResponseRecorder, []scenarioBody, string) {
		rec := adminDo(t, s, http.MethodGet, "/api/v1/scenarios"+query, "")

		var out struct {
			Scenarios  []scenarioBody `json:"scenarios"`
			NextCursor string         `json:"nextCursor"`
		}

		if rec.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		}

		return rec, out.Scenarios, out.NextCursor
	}

	rec, all, next := list("")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, all, 1)
	require.Equal(t, draft.ID, all[0].ID)
	require.Empty(t, next)

	_, drafts, _ := list("?status=draft")
	require.Len(t, drafts, 1)

	_, reviewed, _ := list("?status=reviewed")
	require.Empty(t, reviewed)

	for _, query := range []string{"?status=other", "?limit=0", "?limit=101", "?cursor=" + url.QueryEscape("nope")} {
		rec, _, _ := list(query)
		require.Equal(t, http.StatusBadRequest, rec.Code, query)
	}

	// A viewer cannot list.
	denied := s.do(t, http.MethodGet, "/api/v1/scenarios", viSession, "", nil)
	require.Equal(t, http.StatusForbidden, denied.Code)
}

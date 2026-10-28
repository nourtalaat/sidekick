//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The investigations of fixture/investigations.sql, by the last
// characters of their IDs.
const (
	inv1 = "0192d3a4-0000-7000-8000-0000000000c1"
	inv2 = "0192d3a4-0000-7000-8000-0000000000c2"
	inv3 = "0192d3a4-0000-7000-8000-0000000000c3"
	inv4 = "0192d3a4-0000-7000-8000-0000000000c4"
	inv5 = "0192d3a4-0000-7000-8000-0000000000c5"
)

// investigationRow is a row of GET /investigations, as the tests read it.
type investigationRow struct {
	ID         string  `json:"id"`
	Title      *string `json:"title"`
	Service    *string `json:"service"`
	Route      string  `json:"route"`
	Verdict    *string `json:"verdict"`
	Confidence *string `json:"confidence"`
	Status     string  `json:"status"`
	DurationMs *int64  `json:"durationMs"`
	CostMicros int64   `json:"costMicros"`
	Feedback   struct {
		Correct int    `json:"correct"`
		Wrong   int    `json:"wrong"`
		Summary string `json:"summary"`
	} `json:"feedback"`
	Watcher *struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"watcher"`
}

// investigationList is the body of GET /investigations.
type investigationList struct {
	Investigations []investigationRow `json:"investigations"`
	NextCursor     *string            `json:"nextCursor"`
}

// setupInvestigations returns a server with fixture/investigations.sql
// loaded too.
func setupInvestigations(t *testing.T) server {
	t.Helper()

	s := setup(t)
	storetest.LoadFixture(t, s.pool, "fixture/investigations.sql")

	return s
}

func TestListInvestigations(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	tests := []struct {
		name       string
		session    string
		query      string
		wantStatus int
		wantIDs    []string
		wantMore   bool
	}{
		{name: "every investigation, newest first", session: viSession, wantStatus: http.StatusOK, wantIDs: []string{inv5, inv4, inv3, inv2, inv1}},
		{name: "a first page", session: viSession, query: "?limit=2", wantStatus: http.StatusOK, wantIDs: []string{inv5, inv4}, wantMore: true},
		{name: "a route", session: viSession, query: "?route=payments", wantStatus: http.StatusOK, wantIDs: []string{inv2, inv1}},
		{name: "a service", session: viSession, query: "?service=checkout-api", wantStatus: http.StatusOK, wantIDs: []string{inv3, inv1}},
		{name: "a model", session: viSession, query: "?model=big-model", wantStatus: http.StatusOK, wantIDs: []string{inv3, inv1}},
		{name: "a verdict", session: viSession, query: "?verdict=false_positive", wantStatus: http.StatusOK, wantIDs: []string{inv2}},
		{name: "a status", session: viSession, query: "?status=running", wantStatus: http.StatusOK, wantIDs: []string{inv3}},
		{name: "words of the title", session: viSession, query: "?q=disk", wantStatus: http.StatusOK, wantIDs: []string{inv2}},
		{name: "a phrase of the title", session: viSession, query: "?q=" + url.QueryEscape(`"p99 latency"`), wantStatus: http.StatusOK, wantIDs: []string{inv1}},
		{name: "words that leave a word out", session: viSession, query: "?q=" + url.QueryEscape("checkout -error"), wantStatus: http.StatusOK, wantIDs: []string{inv1}},
		{name: "words no title has", session: viSession, query: "?q=kubernetes", wantStatus: http.StatusOK, wantIDs: []string{}},
		{
			name: "a time range", session: viSession, wantStatus: http.StatusOK, wantIDs: []string{inv3, inv2},
			query: "?from=" + url.QueryEscape("2026-10-26T14:10:00Z") + "&to=" + url.QueryEscape("2026-10-26T14:30:00Z"),
		},
		{name: "filters together", session: viSession, query: "?route=default&service=db", wantStatus: http.StatusOK, wantIDs: []string{inv5}},
		{name: "filters that match nothing together", session: viSession, query: "?route=payments&status=running", wantStatus: http.StatusOK, wantIDs: []string{}},
		{name: "a verdict that is not one", session: viSession, query: "?verdict=maybe", wantStatus: http.StatusBadRequest},
		{name: "a status that is not one", session: viSession, query: "?status=done", wantStatus: http.StatusBadRequest},
		{name: "a limit of zero", session: viSession, query: "?limit=0", wantStatus: http.StatusBadRequest},
		{name: "a limit too high", session: viSession, query: "?limit=201", wantStatus: http.StatusBadRequest},
		{name: "a limit that is not a number", session: viSession, query: "?limit=many", wantStatus: http.StatusBadRequest},
		{name: "a time that is not a time", session: viSession, query: "?from=yesterday", wantStatus: http.StatusBadRequest},
		{name: "a text over the limit", session: viSession, query: "?q=" + strings.Repeat("a", 201), wantStatus: http.StatusBadRequest},
		{name: "a cursor this API did not make", session: viSession, query: "?cursor=bm9wZQ", wantStatus: http.StatusBadRequest},
		{name: "no session", wantStatus: http.StatusUnauthorized},
		{name: "an expired session", session: viExpiredSession, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/investigations"+tt.query, tt.session, "", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			list := decodeInvestigations(t, rec)
			require.Equal(t, tt.wantIDs, investigationIDs(list))
			require.Equal(t, tt.wantMore, list.NextCursor != nil)
		})
	}
}

func TestListInvestigationsWithAToken(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	rec := s.doWithToken(t, http.MethodGet, "/api/v1/investigations", "skt_viewer", nil)
	require.Equal(t, []string{inv5, inv4, inv3, inv2, inv1}, investigationIDs(decodeInvestigations(t, rec)))
}

// TestListInvestigationsPages walks the pages, including two investigations
// created at the same time, and a filter that goes with the cursor.
func TestListInvestigationsPages(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	var got []string

	cursor := ""

	for range 5 {
		query := "/api/v1/investigations?limit=2"
		if cursor != "" {
			query += "&cursor=" + url.QueryEscape(cursor)
		}

		list := decodeInvestigations(t, s.do(t, http.MethodGet, query, viSession, "", nil))
		got = append(got, investigationIDs(list)...)

		if list.NextCursor == nil {
			break
		}

		cursor = *list.NextCursor
	}

	require.Equal(t, []string{inv5, inv4, inv3, inv2, inv1}, got)

	first := decodeInvestigations(t, s.do(t, http.MethodGet, "/api/v1/investigations?route=default&limit=1", viSession, "", nil))
	require.Equal(t, []string{inv5}, investigationIDs(first))
	require.NotNil(t, first.NextCursor)

	second := decodeInvestigations(t, s.do(t, http.MethodGet, "/api/v1/investigations?route=default&limit=1&cursor="+url.QueryEscape(*first.NextCursor), viSession, "", nil))
	require.Equal(t, []string{inv4}, investigationIDs(second))
}

func TestListInvestigationsRows(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	rows := map[string]investigationRow{}

	for _, row := range decodeInvestigations(t, s.do(t, http.MethodGet, "/api/v1/investigations", viSession, "", nil)).Investigations {
		rows[row.ID] = row
	}

	one := rows[inv1]
	require.Equal(t, "Checkout p99 latency above 2s", *one.Title)
	require.Equal(t, "checkout-api", *one.Service)
	require.Equal(t, "payments", one.Route)
	require.Equal(t, "true_positive", *one.Verdict)
	require.Equal(t, "high", *one.Confidence)
	require.Equal(t, "final", one.Status)
	require.EqualValues(t, 180000, *one.DurationMs)
	require.EqualValues(t, 12000, one.CostMicros)
	require.Equal(t, 2, one.Feedback.Correct, "only the ratings of the verdict of the current run count")
	require.Equal(t, 1, one.Feedback.Wrong)
	require.Equal(t, "mixed", one.Feedback.Summary)
	require.Equal(t, "health", one.Watcher.Name)
	require.Equal(t, "recovered", one.Watcher.Status)

	require.Equal(t, "wrong", rows[inv2].Feedback.Summary)
	require.Nil(t, rows[inv2].Watcher)

	three := rows[inv3]
	require.Equal(t, "none", three.Feedback.Summary)
	require.Nil(t, three.DurationMs, "a run that has not ended has no duration")
	require.Equal(t, "running", three.Watcher.Status)

	four := rows[inv4]
	require.Nil(t, four.Title, "an empty title is left out")
	require.Nil(t, four.Service)
	require.Nil(t, four.Verdict)
}

func TestGetInvestigation(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	t.Run("one with a report", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv1)
		require.Equal(t, inv1, body["id"])
		require.Equal(t, "Checkout p99 latency above 2s", body["title"])
		require.Equal(t, "claude", body["connector"])
		require.Equal(t, "big-model", body["model"])
		require.Equal(t, "generic-alert", body["strategy"])
		require.Equal(t, "v1", body["strategyVersion"])
		require.Equal(t, "final", body["status"])
		require.EqualValues(t, 2, body["run"])
		require.Equal(t, "investigate", body["runKind"])
		require.EqualValues(t, 1200, body["inputTokens"])
		require.EqualValues(t, 12000, body["costMicros"])
		require.EqualValues(t, 180000, body["durationMs"])
		require.EqualValues(t, 2, body["eventCount"])
		confirmation, ok := body["confirmation"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "true_positive", confirmation["verdict"])
		require.Equal(t, "Deploy", confirmation["cause"])
		requireTime(t, "2026-10-26T14:30:00Z", confirmation["at"])
		require.Empty(t, body["pendingFollowUps"])

		rep, ok := body["report"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "Deploy 4f2c1a doubled latency.", rep["summary"])
		require.Equal(t, "submitted", rep["stopReason"])

		findings, ok := rep["findings"].([]any)
		require.True(t, ok)
		require.Len(t, findings, 2)

		first, ok := findings[0].(map[string]any)
		require.True(t, ok)

		evidence, ok := first["evidence"].([]any)
		require.True(t, ok)
		require.Len(t, evidence, 2)
		require.Equal(t, "https://signoz.example.com/d/1", evidence[0].(map[string]any)["url"])
		require.NotContains(t, evidence[1], "url", "an evidence link that is not a web link is left out")

		second, ok := findings[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, []any{}, second["evidence"], "a hypothesis has an empty list, not null")

		followUps, ok := rep["followUps"].([]any)
		require.True(t, ok)
		require.Len(t, followUps, 1)
	})

	t.Run("its events", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv1)

		events, ok := body["events"].([]any)
		require.True(t, ok)
		require.Len(t, events, 2)

		first, ok := events[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "Checkout p99 latency above 2s", first["title"])
		require.Equal(t, "alertmanager", first["intake"])
		require.Equal(t, map[string]any{"kind": "bot", "name": "Alertmanager"}, first["sender"])
		require.Equal(t, map[string]any{"team": "payments", "env": "prod"}, first["labels"])
		require.Equal(t, []any{map[string]any{"title": "Runbook", "url": "https://runbooks.example.com/checkout"}}, first["links"],
			"a link that is not a web link is left out")

		second, ok := events[1].(map[string]any)
		require.True(t, ok)
		require.Len(t, second["body"], 2000, "the text is cut")
		require.Equal(t, []any{}, second["links"])
	})

	t.Run("one that runs, with a waiting question", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv3)
		require.Equal(t, "running", body["status"])
		require.NotContains(t, body, "report")
		require.NotContains(t, body, "durationMs")
		pending, ok := body["pendingFollowUps"].([]any)
		require.True(t, ok)
		require.Len(t, pending, 1, "only the question no run has taken")

		question, ok := pending[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "Was it the cache?", question["text"])
		require.Equal(t, "Ana", question["author"])
		requireTime(t, "2026-10-26T14:25:00Z", question["createdAt"])
	})

	t.Run("one that failed", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv4)
		require.Equal(t, "failed", body["status"])
		require.NotContains(t, body, "title")
		require.NotContains(t, body, "service")
	})

	tests := []struct {
		name       string
		path       string
		session    string
		wantStatus int
	}{
		{name: "no such investigation", path: "/api/v1/investigations/0192d3a4-0000-7000-8000-0000000000ff", session: viSession, wantStatus: http.StatusNotFound},
		{name: "an ID that is not a UUID", path: "/api/v1/investigations/one", session: viSession, wantStatus: http.StatusBadRequest},
		{name: "no session", path: "/api/v1/investigations/" + inv1, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, tt.path, tt.session, "", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
		})
	}
}

func TestListInvestigationSteps(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	tests := []struct {
		name       string
		query      string
		wantSeqs   []int
		wantNext   *int
		wantStatus int
	}{
		{name: "every step", wantSeqs: []int{0, 1, 2, 3, 4}},
		{name: "after a step", query: "?after=1", wantSeqs: []int{2, 3, 4}},
		{name: "after the last step", query: "?after=4", wantSeqs: []int{}},
		{name: "a run", query: "?run=2", wantSeqs: []int{4}},
		{name: "a run with no steps", query: "?run=9", wantSeqs: []int{}},
		{name: "a first page", query: "?limit=2", wantSeqs: []int{0, 1}, wantNext: new(1)},
		{name: "a page that ends the list", query: "?after=2&limit=2", wantSeqs: []int{3, 4}},
		{name: "a run and a page", query: "?run=1&limit=2&after=0", wantSeqs: []int{1, 2}, wantNext: new(2)},
		{name: "a limit of zero", query: "?limit=0", wantStatus: http.StatusBadRequest},
		{name: "a limit too high", query: "?limit=501", wantStatus: http.StatusBadRequest},
		{name: "an after below the first step", query: "?after=-2", wantStatus: http.StatusBadRequest},
		{name: "an after that is not a number", query: "?after=x", wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv3+"/steps"+tt.query, viSession, "", nil)

			if tt.wantStatus != 0 {
				require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var list struct {
				Steps []struct {
					Seq int `json:"seq"`
				} `json:"steps"`
				NextAfter *int `json:"nextAfter"`
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

			seqs := make([]int, 0, len(list.Steps))
			for _, step := range list.Steps {
				seqs = append(seqs, step.Seq)
			}

			require.Equal(t, tt.wantSeqs, seqs)
			require.Equal(t, tt.wantNext, list.NextAfter)
		})
	}

	t.Run("the record of a step", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv3+"/steps")

		steps, ok := body["steps"].([]any)
		require.True(t, ok)
		require.Len(t, steps, 5)

		prompt, ok := steps[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "prompt", prompt["kind"])
		require.Equal(t, []any{map[string]any{"type": "text", "text": "Investigate this alert."}}, prompt["parts"],
			"a prompt step has only its first message")

		turn, ok := steps[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "model_turn", turn["kind"])
		require.Equal(t, "claude", turn["connector"])
		require.Equal(t, "big-model", turn["model"])
		require.EqualValues(t, 500, turn["costMicros"])
		require.EqualValues(t, 900, turn["durationMs"])
		require.Equal(t, map[string]any{"input": float64(100), "output": float64(20), "cacheRead": float64(10), "cacheWrite": float64(5)}, turn["tokens"])
		require.Equal(t, []any{
			map[string]any{"type": "reasoning", "text": "Thinking about logs."},
			map[string]any{"type": "text", "text": "I will search."},
			map[string]any{"type": "tool_call", "id": "c1", "name": "search_logs", "input": map[string]any{"query": "level:error"}},
		}, turn["parts"], "reasoning shows its text only, and a reasoning part with no text is left out")

		result, ok := steps[2].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "tool_result", result["kind"])
		require.Equal(t, "signoz", result["source"])
		require.Equal(t, "search_logs", result["tool"])
		require.Equal(t, "c1", result["callId"])
		require.Equal(t, []any{map[string]any{"type": "tool_result", "callId": "c1", "content": "3 errors", "isError": false}}, result["parts"])
	})

	t.Run("nothing the provider signs", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv3+"/steps", viSession, "", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotContains(t, rec.Body.String(), "sig-secret")
		require.NotContains(t, rec.Body.String(), "only-a-signature")
		require.NotContains(t, rec.Body.String(), "SYSTEM SECRET")
	})

	t.Run("an investigation with no steps", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv1+"/steps")
		require.Equal(t, []any{}, body["steps"])
	})

	t.Run("no such investigation", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/0192d3a4-0000-7000-8000-0000000000ff/steps", viSession, "", nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv3+"/steps", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestListInvestigationWatchers(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	t.Run("a recovered watcher with its checks", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv1+"/watchers")

		watchers, ok := body["watchers"].([]any)
		require.True(t, ok)
		require.Len(t, watchers, 1)

		w, ok := watchers[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "0192d3a4-0000-7000-8000-0000000000d1", w["id"])
		require.Equal(t, "health", w["name"])
		require.Equal(t, "http", w["checkType"])
		require.Equal(t, "recovered", w["status"])
		require.EqualValues(t, 3, w["checks"])
		require.EqualValues(t, 2, w["passesInARow"])
		require.EqualValues(t, 30, w["intervalSeconds"])
		require.Equal(t, "HTTP 200", w["lastResult"])
		requireTime(t, "2026-10-26T14:02:30Z", w["lastCheckAt"])
		requireTime(t, "2026-10-26T14:02:30Z", w["endedAt"])

		checks, ok := w["recentChecks"].([]any)
		require.True(t, ok)
		require.Len(t, checks, 3)

		seqs := make([]any, 0, len(checks))
		for _, c := range checks {
			m, ok := c.(map[string]any)
			require.True(t, ok)

			seqs = append(seqs, m["seq"])
		}

		require.Equal(t, []any{float64(3), float64(2), float64(1)}, seqs, "newest first")
		oldest, ok := checks[2].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "fail", oldest["result"])
		require.Equal(t, "HTTP 503", oldest["detail"])
		require.EqualValues(t, 12, oldest["durationMs"])
		requireTime(t, "2026-10-26T14:01:00Z", oldest["at"])
	})

	t.Run("a running watcher", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv3+"/watchers")

		watchers, ok := body["watchers"].([]any)
		require.True(t, ok)
		require.Len(t, watchers, 1)

		w, ok := watchers[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "running", w["status"])
		require.NotContains(t, w, "endedAt")
	})

	t.Run("an investigation with no watcher", func(t *testing.T) {
		t.Parallel()

		body := getJSON(t, s, "/api/v1/investigations/"+inv2+"/watchers")
		require.Equal(t, []any{}, body["watchers"])
	})

	t.Run("only the last 50 checks of a watcher", func(t *testing.T) {
		t.Parallel()

		_, err := s.pool.Exec(t.Context(), `INSERT INTO watcher_checks (watcher_id, seq, at, result, duration_ms)
			SELECT '0192d3a4-0000-7000-8000-0000000000d2', n, '2026-10-26 14:21:00+00'::timestamptz + make_interval(secs => n), 'pass', 1
			FROM generate_series(2, 80) AS n`)
		require.NoError(t, err)

		body := getJSON(t, s, "/api/v1/investigations/"+inv3+"/watchers")

		watchers, ok := body["watchers"].([]any)
		require.True(t, ok)

		w, ok := watchers[0].(map[string]any)
		require.True(t, ok)

		checks, ok := w["recentChecks"].([]any)
		require.True(t, ok)
		require.Len(t, checks, 50)

		first, ok := checks[0].(map[string]any)
		require.True(t, ok)
		require.EqualValues(t, 80, first["seq"])
	})

	t.Run("no such investigation", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/0192d3a4-0000-7000-8000-0000000000ff/watchers", viSession, "", nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("no session", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv1+"/watchers", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

// getJSON reads path as a viewer, and returns its JSON object.
func getJSON(t *testing.T, s server, path string) map[string]any {
	t.Helper()

	rec := s.do(t, http.MethodGet, path, viSession, "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body map[string]any

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func decodeInvestigations(t *testing.T, rec *httptest.ResponseRecorder) investigationList {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list investigationList

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

	return list
}

func investigationIDs(list investigationList) []string {
	ids := make([]string, 0, len(list.Investigations))
	for _, row := range list.Investigations {
		ids = append(ids, row.ID)
	}

	return ids
}

// requireTime checks that got, a time in a JSON body, is the instant of
// want, whichever time zone it is written in.
func requireTime(t *testing.T, want string, got any) {
	t.Helper()

	text, ok := got.(string)
	require.True(t, ok, "%v is not a time", got)

	gotTime, err := time.Parse(time.RFC3339Nano, text)
	require.NoError(t, err)

	wantTime, err := time.Parse(time.RFC3339, want)
	require.NoError(t, err)

	require.True(t, gotTime.Equal(wantTime), "%s is not %s", text, want)
}

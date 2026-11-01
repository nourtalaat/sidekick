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

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/memory"
)

// The memory entries of fixture/memory.sql, newest confirmation first.
const (
	memE1         = "0192d3a4-0000-7000-8000-0000000000e1"
	memE2         = "0192d3a4-0000-7000-8000-0000000000e2"
	memE3         = "0192d3a4-0000-7000-8000-0000000000e3"
	memE4         = "0192d3a4-0000-7000-8000-0000000000e4"
	missingMemory = "0192d3a4-0000-7000-8000-0000000000ee"
)

// memoryEntryBody is a memory entry as the API returns it.
type memoryEntryBody struct {
	InvestigationID string  `json:"investigationId"`
	Service         *string `json:"service"`
	AlertName       *string `json:"alertName"`
	Verdict         string  `json:"verdict"`
	Summary         string  `json:"summary"`
	Cause           *string `json:"cause"`
	ConfirmedAt     string  `json:"confirmedAt"`
	Disabled        bool    `json:"disabled"`
}

// memoryListBody is the body of GET /memory/entries.
type memoryListBody struct {
	Entries    []memoryEntryBody `json:"entries"`
	NextCursor *string           `json:"nextCursor"`
}

// setupMemory returns a server with the investigations and the memory
// fixtures loaded, and the memory store set up with vector memory on.
func setupMemory(t *testing.T) server {
	t.Helper()

	return setupWith(t, func(o *api.Options) {
		o.MemoryStore = memory.NewStore(o.Pool, o.Redactor, memory.SyncOptions{Embed: true})
	}, "fixture/rules_api.sql", "fixture/investigations.sql", "fixture/memory.sql")
}

func decodeMemoryList(t *testing.T, rec *httptest.ResponseRecorder) memoryListBody {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var l memoryListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l))

	return l
}

func decodeMemoryEntry(t *testing.T, rec *httptest.ResponseRecorder, status int) memoryEntryBody {
	t.Helper()

	require.Equal(t, status, rec.Code, rec.Body.String())

	var e memoryEntryBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e))

	return e
}

func memoryIDsOf(l memoryListBody) []string {
	out := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		out[i] = e.InvestigationID
	}

	return out
}

// auditCount counts the audit rows with the action about target.
func (s server) auditCount(t *testing.T, action, target string) int {
	t.Helper()

	var n int

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE action = $1 AND target_id = $2", action, target).Scan(&n))

	return n
}

func TestListMemoryEntries(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "every entry, newest confirmation first", want: []string{memE1, memE2, memE3, memE4}},
		{name: "one service", query: "?service=checkout-api", want: []string{memE1, memE2}},
		{name: "one verdict", query: "?verdict=flapping", want: []string{memE2}},
		{name: "words", query: "?q=" + url.QueryEscape("batch export"), want: []string{memE3}},
		{name: "words of the cause", query: "?q=slow+query", want: []string{memE1}},
		{name: "only disabled", query: "?disabled=true", want: []string{memE3}},
		{name: "only enabled", query: "?disabled=false", want: []string{memE1, memE2, memE4}},
		{name: "filters that exclude each other", query: "?service=orders-api&disabled=false"},
		{name: "a page of two", query: "?limit=2", want: []string{memE1, memE2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list := decodeMemoryList(t, s.do(t, http.MethodGet, "/api/v1/memory/entries"+tt.query, viSession, viCSRF, nil))
			require.Equal(t, tt.want, append([]string(nil), memoryIDsOf(list)...))
		})
	}

	t.Run("the fields of an entry", func(t *testing.T) {
		t.Parallel()

		list := decodeMemoryList(t, s.do(t, http.MethodGet, "/api/v1/memory/entries?limit=1", viSession, viCSRF, nil))
		require.Len(t, list.Entries, 1)

		e := list.Entries[0]
		require.Equal(t, memE1, e.InvestigationID)
		require.Equal(t, "checkout-api", *e.Service)
		require.Equal(t, "CheckoutLatencyP99", *e.AlertName)
		require.Equal(t, "true_positive", e.Verdict)
		require.Equal(t, "p99 latency rose after a deploy.", e.Summary)
		require.Equal(t, "Deploy 4f2c1a added a slow query.", *e.Cause)

		confirmed, err := time.Parse(time.RFC3339, e.ConfirmedAt)
		require.NoError(t, err)
		require.True(t, confirmed.Equal(time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)), "confirmed at %s", e.ConfirmedAt)
		require.False(t, e.Disabled)
		require.NotNil(t, list.NextCursor)

		last := decodeMemoryList(t, s.do(t, http.MethodGet, "/api/v1/memory/entries", viSession, viCSRF, nil)).Entries[3]
		require.Nil(t, last.Service)
		require.Nil(t, last.AlertName)
		require.Nil(t, last.Cause)
	})

	t.Run("pages follow each other", func(t *testing.T) {
		t.Parallel()

		var (
			got  []string
			path = "/api/v1/memory/entries?limit=3"
		)

		for range 3 {
			list := decodeMemoryList(t, s.do(t, http.MethodGet, path, viSession, viCSRF, nil))
			got = append(got, memoryIDsOf(list)...)

			if list.NextCursor == nil {
				break
			}

			path = "/api/v1/memory/entries?limit=3&cursor=" + url.QueryEscape(*list.NextCursor)
		}

		require.Equal(t, []string{memE1, memE2, memE3, memE4}, got)
	})

	for _, tt := range []struct{ name, query string }{
		{name: "a limit of zero", query: "?limit=0"},
		{name: "a limit that is too big", query: "?limit=201"},
		{name: "a cursor that is not one", query: "?cursor=abc"},
		{name: "a verdict that does not exist", query: "?verdict=maybe"},
		{name: "a disabled that is not a boolean", query: "?disabled=maybe"},
		{name: "a query that is too long", query: "?q=" + strings.Repeat("a", 201)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/memory/entries"+tt.query, viSession, viCSRF, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestMemoryRoles(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	body := map[string]any{"summary": "A summary.", "disabled": false}

	writes := []struct {
		name         string
		method, path string
		body         any
	}{
		{name: "update", method: http.MethodPut, path: "/api/v1/memory/entries/" + memE1, body: body},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/memory/entries/" + memE1},
	}

	for _, w := range writes {
		for _, c := range []ruleCaller{viewerCaller, responderCaller, viewerTokenCaller, responderTokenCaller} {
			t.Run(w.name+" as "+c.name, func(t *testing.T) {
				t.Parallel()

				rec := s.sendRule(t, c, w.method, w.path, w.body)
				require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			})
		}
	}

	for _, c := range []ruleCaller{viewerCaller, responderCaller, viewerTokenCaller, adminCaller} {
		t.Run("read as "+c.name, func(t *testing.T) {
			t.Parallel()

			require.Len(t, decodeMemoryList(t, s.sendRule(t, c, http.MethodGet, "/api/v1/memory/entries", nil)).Entries, 4)
			require.Equal(t, memE1, decodeMemoryEntry(t, s.sendRule(t, c, http.MethodGet, "/api/v1/memory/entries/"+memE1, nil), http.StatusOK).InvestigationID)
		})
	}

	t.Run("nobody signed in", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/memory/entries", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("a write without the CSRF token", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodDelete, "/api/v1/memory/entries/"+memE1, adSession, "", nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("a refused write changes nothing", func(t *testing.T) {
		t.Parallel()

		require.Zero(t, s.auditCount(t, "memory.updated", memE1))
		require.Zero(t, s.auditCount(t, "memory.deleted", memE1))
	})
}

func TestGetMemoryEntry(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	e := decodeMemoryEntry(t, s.do(t, http.MethodGet, "/api/v1/memory/entries/"+memE3, viSession, viCSRF, nil), http.StatusOK)
	require.Equal(t, memE3, e.InvestigationID)
	require.True(t, e.Disabled)
	require.Equal(t, "false_positive", e.Verdict)

	t.Run("an entry that does not exist", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/memory/entries/"+missingMemory, viSession, viCSRF, nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Equal(t, "not_found", ruleProblemOf(t, rec).Code)
	})

	t.Run("an ID that is not one", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/memory/entries/abc", viSession, viCSRF, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestUpdateMemoryEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		caller     ruleCaller
		id         string
		body       map[string]any
		wantStatus int
		wantAction string
		wantActor  string
		wantEntry  *memoryEntryBody
		// wantField is the field of a 422.
		wantField string
	}{
		{
			name: "new text", caller: adminCaller, id: memE1, body: map[string]any{"summary": " Pool exhausted. ", "cause": "Pool of 10.", "disabled": false},
			wantStatus: http.StatusOK, wantAction: "memory.updated", wantActor: "user",
			wantEntry: &memoryEntryBody{Summary: "Pool exhausted.", Cause: new("Pool of 10."), Disabled: false},
		},
		{
			name: "a cause taken away", caller: adminCaller, id: memE1, body: map[string]any{"summary": "p99 latency rose after a deploy.", "disabled": false},
			wantStatus: http.StatusOK, wantAction: "memory.updated", wantActor: "user",
			wantEntry: &memoryEntryBody{Summary: "p99 latency rose after a deploy."},
		},
		{
			name: "disabled by a token", caller: adminTokenCaller, id: memE1,
			body:       map[string]any{"summary": "p99 latency rose after a deploy.", "cause": "Deploy 4f2c1a added a slow query.", "disabled": true},
			wantStatus: http.StatusOK, wantAction: "memory.disabled", wantActor: "token",
			wantEntry: &memoryEntryBody{Summary: "p99 latency rose after a deploy.", Cause: new("Deploy 4f2c1a added a slow query."), Disabled: true},
		},
		{
			name: "enabled", caller: adminCaller, id: memE3, body: map[string]any{"summary": "Only the batch export endpoint was slow.", "disabled": false},
			wantStatus: http.StatusOK, wantAction: "memory.enabled", wantActor: "user",
			wantEntry: &memoryEntryBody{Summary: "Only the batch export endpoint was slow."},
		},
		{
			name: "an empty summary", caller: adminCaller, id: memE1, body: map[string]any{"summary": "  ", "disabled": false},
			wantStatus: http.StatusUnprocessableEntity, wantField: "summary",
		},
		{
			name: "a summary that is too long", caller: adminCaller, id: memE1, body: map[string]any{"summary": strings.Repeat("a", 2001), "disabled": false},
			wantStatus: http.StatusUnprocessableEntity, wantField: "summary",
		},
		{
			name: "a cause with control characters", caller: adminCaller, id: memE1, body: map[string]any{"summary": "A summary.", "cause": "a\u0000b", "disabled": false},
			wantStatus: http.StatusUnprocessableEntity, wantField: "cause",
		},
		{
			name: "an entry that does not exist", caller: adminCaller, id: missingMemory, body: map[string]any{"summary": "A summary.", "disabled": false},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "a body without a summary", caller: adminCaller, id: memE1, body: map[string]any{"disabled": false},
			wantStatus: http.StatusUnprocessableEntity, wantField: "summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupMemory(t)

			rec := s.sendRule(t, tt.caller, http.MethodPut, "/api/v1/memory/entries/"+tt.id, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				if tt.wantField != "" {
					require.Contains(t, ruleProblemOf(t, rec).fieldMessages(), tt.wantField)
				}

				require.Zero(t, s.auditCount(t, "memory.updated", tt.id)+s.auditCount(t, "memory.disabled", tt.id)+s.auditCount(t, "memory.enabled", tt.id))

				return
			}

			e := decodeMemoryEntry(t, rec, http.StatusOK)
			require.Equal(t, tt.id, e.InvestigationID)
			require.Equal(t, tt.wantEntry.Summary, e.Summary)
			require.Equal(t, tt.wantEntry.Cause, e.Cause)
			require.Equal(t, tt.wantEntry.Disabled, e.Disabled)

			require.Equal(t, 1, s.auditCount(t, tt.wantAction, tt.id))

			var (
				actor string
				inv   string
			)

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT actor_kind, investigation_id::text FROM audit_log WHERE action = $1 AND target_id = $2", tt.wantAction, tt.id).Scan(&actor, &inv))
			require.Equal(t, tt.wantActor, actor)
			require.Equal(t, tt.id, inv)

			stored := decodeMemoryEntry(t, s.do(t, http.MethodGet, "/api/v1/memory/entries/"+tt.id, viSession, viCSRF, nil), http.StatusOK)
			require.Equal(t, e, stored, "the answer is the stored entry")
		})
	}

	t.Run("an edit that changes nothing writes no audit entry", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		rec := s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/memory/entries/"+memE1,
			map[string]any{"summary": "p99 latency rose after a deploy.", "cause": "Deploy 4f2c1a added a slow query.", "disabled": false})
		require.Equal(t, http.StatusOK, rec.Code)

		var n int

		require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE target_kind = 'memory_entry'").Scan(&n))
		require.Zero(t, n)
	})

	t.Run("a text change enqueues the embed job, a flag change does not", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		jobs := func() int {
			var n int

			require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'embed_memory'").Scan(&n))

			return n
		}

		rec := s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/memory/entries/"+memE1,
			map[string]any{"summary": "p99 latency rose after a deploy.", "cause": "Deploy 4f2c1a added a slow query.", "disabled": true})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Zero(t, jobs())

		rec = s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/memory/entries/"+memE1,
			map[string]any{"summary": "New words.", "cause": "Deploy 4f2c1a added a slow query.", "disabled": true})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 1, jobs())
	})

	t.Run("the text is redacted before it is stored", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		// The test server redacts email addresses, as sidekick serve does with
		// the rules of the config.
		const address = "ana@example.com"

		e := decodeMemoryEntry(t, s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/memory/entries/"+memE1,
			map[string]any{"summary": "Ask " + address + " about it.", "cause": "Mail " + address, "disabled": false}), http.StatusOK)
		require.NotContains(t, e.Summary, address)
		require.Contains(t, e.Summary, "[REDACTED:")
		require.Contains(t, *e.Cause, "[REDACTED:")

		var audited int

		require.NoError(t, s.pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_log WHERE target_kind = 'memory_entry' AND details::text LIKE '%' || $1 || '%'", address).Scan(&audited))
		require.Zero(t, audited, "the audit log never holds the address")
	})
}

func TestDeleteMemoryEntry(t *testing.T) {
	t.Parallel()

	for _, c := range []ruleCaller{adminCaller, adminTokenCaller} {
		t.Run("as "+c.name, func(t *testing.T) {
			t.Parallel()

			s := setupMemory(t)

			rec := s.sendRule(t, c, http.MethodDelete, "/api/v1/memory/entries/"+memE2, nil)
			require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

			require.Equal(t, http.StatusNotFound, s.do(t, http.MethodGet, "/api/v1/memory/entries/"+memE2, viSession, viCSRF, nil).Code)
			require.Len(t, decodeMemoryList(t, s.do(t, http.MethodGet, "/api/v1/memory/entries", viSession, viCSRF, nil)).Entries, 3)
			require.Equal(t, 1, s.auditCount(t, "memory.deleted", memE2))

			var summary string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT details->'old'->>'summary' FROM audit_log WHERE action = 'memory.deleted' AND target_id = $1", memE2).Scan(&summary))
			require.Equal(t, "Errors came and went for ten minutes.", summary)
		})
	}

	t.Run("an entry that does not exist", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		rec := s.sendRule(t, adminCaller, http.MethodDelete, "/api/v1/memory/entries/"+missingMemory, nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
		require.Zero(t, s.auditCount(t, "memory.deleted", missingMemory))
	})
}

func TestMemoryWithoutAStore(t *testing.T) {
	t.Parallel()

	s := setupWith(t, nil, "fixture/rules_api.sql")

	rec := s.do(t, http.MethodGet, "/api/v1/memory/entries", viSession, viCSRF, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

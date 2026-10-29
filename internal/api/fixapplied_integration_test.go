//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// fixAppliedBody is the response of PUT and DELETE
// /investigations/{id}/fix-applied.
type fixAppliedBody struct {
	Applied   bool       `json:"applied"`
	Changed   bool       `json:"changed"`
	AppliedAt *time.Time `json:"appliedAt"`
}

func TestMarkFixApplied(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	_, err := s.pool.Exec(t.Context(), "DELETE FROM rollup_dirty")
	require.NoError(t, err)

	// Marking the fix of inv2, a false positive, records who and when.
	first := putFix(t, s, http.MethodPut, inv2, faSession, faCSRF)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	got := readFixBody(t, first)
	require.True(t, got.Applied)
	require.True(t, got.Changed)
	require.NotNil(t, got.AppliedAt)
	require.Equal(t, []fixAuditRow{{Actor: "user " + userFa, Action: "fix.applied", Old: false, New: true}}, fixAuditRows(t, s.pool, inv2))
	require.Equal(t, []string{"stats_hourly 2026-10-26 14:00"}, dirtyStats(t, s.pool))

	// The investigation shows when.
	detail := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv2, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, detail.Code)

	var inv struct {
		FixAppliedAt *time.Time `json:"fixAppliedAt"`
	}

	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &inv))
	require.NotNil(t, inv.FixAppliedAt)
	require.WithinDuration(t, *got.AppliedAt, *inv.FixAppliedAt, time.Millisecond)

	// Marking it again by someone else changes nothing and writes no audit row.
	again := putFix(t, s, http.MethodPut, inv2, fbSession, fbCSRF)
	require.Equal(t, http.StatusOK, again.Code)

	got2 := readFixBody(t, again)
	require.True(t, got2.Applied)
	require.False(t, got2.Changed)
	require.NotNil(t, got2.AppliedAt)
	require.WithinDuration(t, *got.AppliedAt, *got2.AppliedAt, time.Millisecond)
	require.Len(t, fixAuditRows(t, s.pool, inv2), 1)

	// Taking the mark back.
	back := putFix(t, s, http.MethodDelete, inv2, fbSession, fbCSRF)
	require.Equal(t, http.StatusOK, back.Code)
	require.Equal(t, fixAppliedBody{Applied: false, Changed: true}, readFixBody(t, back))
	require.Equal(t, []fixAuditRow{
		{Actor: "user " + userFa, Action: "fix.applied", Old: false, New: true},
		{Actor: "user " + userFb, Action: "fix.unapplied", Old: true, New: false},
	}, fixAuditRows(t, s.pool, inv2))

	// Taking back a mark that is not there changes nothing.
	none := putFix(t, s, http.MethodDelete, inv2, fbSession, fbCSRF)
	require.Equal(t, http.StatusOK, none.Code)
	require.Equal(t, fixAppliedBody{Applied: false, Changed: false}, readFixBody(t, none))
	require.Len(t, fixAuditRows(t, s.pool, inv2), 2)
}

func TestMarkFixAppliedRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		method     string
		id         string
		session    string
		csrf       string
		wantStatus int
		wantDetail string
	}{
		{
			name: "a true positive has no fix to mark", method: http.MethodPut, id: inv1, session: faSession, csrf: faCSRF,
			wantStatus: http.StatusConflict, wantDetail: "Only an investigation that ended as a false positive has a fix to mark as applied.",
		},
		{
			name: "a running investigation has no fix to mark", method: http.MethodPut, id: inv3, session: faSession, csrf: faCSRF,
			wantStatus: http.StatusConflict, wantDetail: "Only an investigation that ended as a false positive has a fix to mark as applied.",
		},
		{
			name: "an investigation that does not exist", method: http.MethodPut, id: "0192d3a4-0000-7000-8000-0000000000ff", session: faSession, csrf: faCSRF,
			wantStatus: http.StatusNotFound, wantDetail: "No investigation has this ID.",
		},
		{
			name: "a viewer cannot mark", method: http.MethodPut, id: inv2, session: viSession, csrf: viCSRF,
			wantStatus: http.StatusForbidden, wantDetail: "This needs the responder role.",
		},
		{
			name: "a viewer cannot take it back", method: http.MethodDelete, id: inv2, session: viSession, csrf: viCSRF,
			wantStatus: http.StatusForbidden, wantDetail: "This needs the responder role.",
		},
		{
			name: "no CSRF token", method: http.MethodPut, id: inv2, session: faSession,
			wantStatus: http.StatusForbidden, wantDetail: "Send the session's CSRF token in the X-CSRF-Token header.",
		},
		{
			name: "no session", method: http.MethodPut, id: inv2,
			wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupFeedback(t)

			rec := putFix(t, s, tt.method, tt.id, tt.session, tt.csrf)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
			require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, "/api/v1/investigations/"+tt.id+"/fix-applied"), rec.Body.String())
			require.Empty(t, fixAuditRows(t, s.pool, tt.id))
		})
	}
}

func TestMarkFixAppliedWithAnAPIToken(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	const tokenID = "01920000-0000-7000-8000-000000000043"

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
		 VALUES ('`+tokenID+`', 'responder bot', sha256(convert_to('skt_responder', 'UTF8')), 'responder',
		         '01920000-0000-7000-8000-000000000040', now(), now() + interval '30 days')`)
	require.NoError(t, err)

	rec := s.doWithToken(t, http.MethodPut, "/api/v1/investigations/"+inv2+"/fix-applied", "skt_responder", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []fixAuditRow{{Actor: "token " + tokenID, Action: "fix.applied", Old: false, New: true}}, fixAuditRows(t, s.pool, inv2))
}

// putFix sends a PUT or DELETE to /investigations/{id}/fix-applied.
func putFix(t *testing.T, s server, method, id, session, csrf string) *httptest.ResponseRecorder {
	t.Helper()

	return s.do(t, method, "/api/v1/investigations/"+id+"/fix-applied", session, csrf, nil)
}

func readFixBody(t *testing.T, rec *httptest.ResponseRecorder) fixAppliedBody {
	t.Helper()

	var got fixAppliedBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	return got
}

// fixAuditRow is an audit row of the fix mark, as the tests compare it.
type fixAuditRow struct {
	Actor, Action string
	Old, New      bool
}

func fixAuditRows(t *testing.T, pool *pgxpool.Pool, id string) []fixAuditRow {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT actor_kind || ' ' || actor_id, action, (details->'old'->>'applied')::boolean, (details->'new'->>'applied')::boolean
		 FROM audit_log WHERE investigation_id = $1 AND action LIKE 'fix.%' ORDER BY id`, id)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fixAuditRow])
	require.NoError(t, err)

	return out
}

// dirtyStats returns the stats_hourly marks that wait, as "table hour".
func dirtyStats(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT table_name || ' ' || to_char(bucket AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI')
		 FROM rollup_dirty WHERE table_name = 'stats_hourly' AND bucket = '2026-10-26 14:00:00+00' ORDER BY 1`)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return out
}

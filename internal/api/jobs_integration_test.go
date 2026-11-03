//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// jobList is the body of GET /jobs, as the tests read it.
type jobList struct {
	Jobs []struct {
		ID        int64   `json:"id"`
		Kind      string  `json:"kind"`
		LastError *string `json:"lastError"`
	} `json:"jobs"`
	NextCursor *string `json:"nextCursor"`
}

// setupJobs returns a server with fixture/jobs.sql loaded too.
func setupJobs(t *testing.T) server {
	t.Helper()

	s := setup(t)
	storetest.LoadFixture(t, s.pool, "fixture/jobs.sql")

	return s
}

func TestListJobs(t *testing.T) {
	t.Parallel()

	s := setupJobs(t)

	tests := []struct {
		name       string
		session    string
		query      string
		wantStatus int
		wantIDs    []int64
		wantMore   bool
	}{
		{name: "every dead job, newest first", session: adSession, wantStatus: http.StatusOK, wantIDs: []int64{3, 2, 1, 5}},
		{name: "status dead", session: adSession, query: "?status=dead", wantStatus: http.StatusOK, wantIDs: []int64{3, 2, 1, 5}},
		{name: "a first page", session: adSession, query: "?limit=2", wantStatus: http.StatusOK, wantIDs: []int64{3, 2}, wantMore: true},
		{name: "another status", session: adSession, query: "?status=pending", wantStatus: http.StatusBadRequest},
		{name: "a limit too high", session: adSession, query: "?limit=101", wantStatus: http.StatusBadRequest},
		{name: "a limit that is not a number", session: adSession, query: "?limit=many", wantStatus: http.StatusBadRequest},
		{name: "a cursor this API did not make", session: adSession, query: "?cursor=bm9wZQ", wantStatus: http.StatusBadRequest},
		{name: "a viewer is denied", session: viSession, wantStatus: http.StatusForbidden},
		{name: "no session", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/jobs"+tt.query, tt.session, "", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			list := decodeJobs(t, rec)
			require.Equal(t, tt.wantIDs, idsOf(list))
			require.Equal(t, tt.wantMore, list.NextCursor != nil)
		})
	}
}

func TestListJobsPages(t *testing.T) {
	t.Parallel()

	s := setupJobs(t)

	first := decodeJobs(t, s.do(t, http.MethodGet, "/api/v1/jobs?limit=3", adSession, "", nil))
	require.Equal(t, []int64{3, 2, 1}, idsOf(first))
	require.NotNil(t, first.NextCursor)

	second := decodeJobs(t, s.do(t, http.MethodGet, "/api/v1/jobs?limit=3&cursor="+url.QueryEscape(*first.NextCursor), adSession, "", nil))
	require.Equal(t, []int64{5}, idsOf(second))
	require.Nil(t, second.NextCursor)

	require.NotNil(t, first.Jobs[2].LastError)
	require.NotContains(t, *first.Jobs[2].LastError, "ana@example.com", "the last error is redacted")
}

func TestRetryJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		session    string
		csrf       string
		token      string
		wantStatus int
		wantActor  string
	}{
		{name: "an admin retries a dead job", id: "3", session: adSession, csrf: adCSRF, wantStatus: http.StatusNoContent, wantActor: "user"},
		{name: "an admin token retries a dead job", id: "3", token: "skt_admin", wantStatus: http.StatusNoContent, wantActor: "token"},
		{name: "no CSRF token", id: "3", session: adSession, wantStatus: http.StatusForbidden},
		{name: "a viewer is denied", id: "3", session: viSession, csrf: viCSRF, wantStatus: http.StatusForbidden},
		{name: "a job that is not dead", id: "4", session: adSession, csrf: adCSRF, wantStatus: http.StatusConflict},
		{name: "a job whose key another job holds", id: "5", session: adSession, csrf: adCSRF, wantStatus: http.StatusConflict},
		{name: "no such job", id: "99", session: adSession, csrf: adCSRF, wantStatus: http.StatusNotFound},
		{name: "an ID that is not a number", id: "three", session: adSession, csrf: adCSRF, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupJobs(t)
			path := "/api/v1/jobs/" + tt.id + "/retry"

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodPost, path, tt.token, nil)
			} else {
				rec = s.do(t, http.MethodPost, path, tt.session, tt.csrf, nil)
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var audited int

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM audit_log WHERE action = 'job.retried' AND target_id = $1", tt.id).Scan(&audited))

			if tt.wantStatus != http.StatusNoContent {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
				require.Zero(t, audited)

				return
			}

			var status string

			var attempts int

			require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status, attempts FROM jobs WHERE id = $1", tt.id).Scan(&status, &attempts))
			require.Equal(t, "pending", status)
			require.Zero(t, attempts)

			var actorKind string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT actor_kind FROM audit_log WHERE action = 'job.retried' AND target_id = $1", tt.id).Scan(&actorKind))
			require.Equal(t, tt.wantActor, actorKind)
		})
	}
}

func TestDiscardJob(t *testing.T) {
	t.Parallel()

	const (
		notDeadSuffix = ", not dead; only dead jobs can be discarded."
		noSuchJob     = "No job has this ID."
	)

	tests := []struct {
		name       string
		id         string
		session    string
		csrf       string
		token      string
		wantStatus int
		wantActor  string
		wantDetail string
	}{
		{name: "an admin discards a dead job", id: "3", session: adSession, csrf: adCSRF, wantStatus: http.StatusNoContent, wantActor: "user"},
		{name: "an admin token discards a dead job", id: "3", token: "skt_admin", wantStatus: http.StatusNoContent, wantActor: "token"},
		{name: "a dead job whose last error holds an email", id: "1", session: adSession, csrf: adCSRF, wantStatus: http.StatusNoContent, wantActor: "user"},
		{name: "no CSRF token", id: "3", session: adSession, wantStatus: http.StatusForbidden},
		{name: "a viewer is denied", id: "3", session: viSession, csrf: viCSRF, wantStatus: http.StatusForbidden},
		{name: "a pending job", id: "4", session: adSession, csrf: adCSRF, wantStatus: http.StatusConflict, wantDetail: "The job is pending" + notDeadSuffix},
		{name: "a running job", id: "7", session: adSession, csrf: adCSRF, wantStatus: http.StatusConflict, wantDetail: "The job is running" + notDeadSuffix},
		{name: "no such job", id: "99", session: adSession, csrf: adCSRF, wantStatus: http.StatusNotFound, wantDetail: noSuchJob},
		{name: "an ID that is not a number", id: "three", session: adSession, csrf: adCSRF, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupJobs(t)
			path := "/api/v1/jobs/" + tt.id + "/discard"

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodPost, path, tt.token, nil)
			} else {
				rec = s.do(t, http.MethodPost, path, tt.session, tt.csrf, nil)
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var audited int

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM audit_log WHERE action = 'job.discarded'").Scan(&audited))

			var exists bool

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT EXISTS (SELECT 1 FROM jobs WHERE id::text = $1)", tt.id).Scan(&exists), "id %s", tt.id)

			if tt.wantStatus != http.StatusNoContent {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
				require.Zero(t, audited)

				if tt.wantDetail != "" {
					require.Equal(t, tt.wantDetail, problemDetail(t, rec))
				}

				if tt.wantStatus != http.StatusNotFound && tt.wantStatus != http.StatusBadRequest {
					require.True(t, exists, "the job stays")
				}

				return
			}

			require.False(t, exists, "the job is deleted")
			require.Equal(t, 1, audited)

			var actorKind, details string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT actor_kind, details::text FROM audit_log WHERE action = 'job.discarded' AND target_kind = 'job' AND target_id = $1", tt.id).
				Scan(&actorKind, &details))
			require.Equal(t, tt.wantActor, actorKind)
			require.NotContains(t, details, "ana@example.com", "the last error is masked")
		})
	}
}

func TestDiscardJobAudit(t *testing.T) {
	t.Parallel()

	s := setupJobs(t)

	rec := s.do(t, http.MethodPost, "/api/v1/jobs/2/discard", adSession, adCSRF, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	var details string

	require.NoError(t, s.pool.QueryRow(t.Context(),
		"SELECT details::text FROM audit_log WHERE action = 'job.discarded' AND target_id = '2'").Scan(&details))
	require.JSONEq(t, `{"old": {"kind": "investigate", "attempts": 5, "lastError": "model: overloaded"}}`, details)
}

// TestDiscardJobRollsBackWithItsAudit makes the audit write fail, and
// checks that the delete is undone with it. The trigger lives in the
// database of this test alone, which setupJobs makes new and which is
// dropped when the test ends.
func TestDiscardJobRollsBackWithItsAudit(t *testing.T) {
	t.Parallel()

	s := setupJobs(t)

	_, err := s.pool.Exec(t.Context(), `
		CREATE FUNCTION refuse_discard_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'audit refused'; END $$;
		CREATE TRIGGER refuse_discard_audit BEFORE INSERT ON audit_log
		FOR EACH ROW WHEN (NEW.action = 'job.discarded') EXECUTE FUNCTION refuse_discard_audit();`)
	require.NoError(t, err)

	rec := s.do(t, http.MethodPost, "/api/v1/jobs/3/discard", adSession, adCSRF, nil)
	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())

	var status string

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status FROM jobs WHERE id = 3").Scan(&status), "the job stays")
	require.Equal(t, "dead", status)
}

// TestDiscardJobTakenMeanwhile holds the row of a dead job in a transaction
// that makes it running, starts a discard that waits for that lock, and
// commits. The discard must then answer 409 and leave the job.
func TestDiscardJobTakenMeanwhile(t *testing.T) {
	t.Parallel()

	s := setupJobs(t)

	holder, err := s.pool.Begin(t.Context())
	require.NoError(t, err)

	defer func() { _ = holder.Rollback(t.Context()) }()

	_, err = holder.Exec(t.Context(), "UPDATE jobs SET status = 'running', finished_at = NULL WHERE id = 3")
	require.NoError(t, err)

	answer := make(chan *httptest.ResponseRecorder, 1)

	go func() {
		answer <- s.do(t, http.MethodPost, "/api/v1/jobs/3/discard", adSession, adCSRF, nil)
	}()

	require.Eventually(t, func() bool {
		var waiting bool

		err := s.pool.QueryRow(t.Context(), `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity
			  WHERE datname = current_database() AND pid <> pg_backend_pid()
			    AND wait_event_type = 'Lock' AND query LIKE '%DELETE FROM jobs%')`).Scan(&waiting)

		return err == nil && waiting
	}, testWait, testPoll, "the discard never waited for the lock on the job")

	require.NoError(t, holder.Commit(t.Context()))

	select {
	case rec := <-answer:
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		require.Equal(t, "The job is running, not dead; only dead jobs can be discarded.", problemDetail(t, rec))
	case <-time.After(testWait):
		require.FailNow(t, fmt.Sprintf("the discard did not answer within %s after the lock was released", testWait))
	}

	var status string

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status FROM jobs WHERE id = 3").Scan(&status), "the job stays")
	require.Equal(t, "running", status)
}

// problemDetail is the detail of a problem document answer.
func problemDetail(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Detail string `json:"detail"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body.Detail
}

func decodeJobs(t *testing.T, rec *httptest.ResponseRecorder) jobList {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var list jobList

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

	return list
}

func idsOf(list jobList) []int64 {
	ids := make([]int64, 0, len(list.Jobs))
	for _, j := range list.Jobs {
		ids = append(ids, j.ID)
	}

	return ids
}

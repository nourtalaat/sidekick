//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// The watchers of fixture/investigations.sql: d1 has recovered, and d2
// still runs for inv3.
const (
	watcherRecovered = "0192d3a4-0000-7000-8000-0000000000d1"
	watcherRunning   = "0192d3a4-0000-7000-8000-0000000000d2"
)

// requestAs is who sends a request in the tests of the actions.
type requestAs int

const (
	asAdmin requestAs = iota
	asViewer
	asToken
	asAdminWithoutCSRF
)

// post sends a POST with body as the caller who.
func (s server) post(t *testing.T, path string, who requestAs, body string) *httptest.ResponseRecorder {
	t.Helper()

	switch who {
	case asViewer:
		return s.do(t, http.MethodPost, path, viSession, viCSRF, strings.NewReader(body))
	case asToken:
		return s.doWithToken(t, http.MethodPost, path, adminToken, strings.NewReader(body))
	case asAdminWithoutCSRF:
		return s.do(t, http.MethodPost, path, adSession, "", strings.NewReader(body))
	default:
		return s.do(t, http.MethodPost, path, adSession, adCSRF, strings.NewReader(body))
	}
}

// setupActions returns a server with the fixtures of the investigations
// and a watcher stopper.
func setupActions(t *testing.T) server {
	t.Helper()

	return setupInvestigationsWith(t, func(opts *api.Options) {
		stopper, err := watch.NewStopper(watch.StopperOptions{Pool: opts.Pool, CloseGroup: grouper.EnqueueClose})
		require.NoError(t, err)

		opts.Stopper = stopper
	})
}

// auditRows returns the actor kind and ID of every audit row with action,
// oldest first.
func (s server) auditRows(t *testing.T, action string) [][2]string {
	t.Helper()

	rows, err := s.pool.Query(t.Context(), "SELECT actor_kind, coalesce(actor_id, '') FROM audit_log WHERE action = $1 ORDER BY id", action)
	require.NoError(t, err)

	defer rows.Close()

	var out [][2]string

	for rows.Next() {
		var row [2]string

		require.NoError(t, rows.Scan(&row[0], &row[1]))

		out = append(out, row)
	}

	require.NoError(t, rows.Err())

	return out
}

func TestAskFollowUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		who  requestAs
		body string
		// prepare changes the data before the request.
		prepare    string
		wantStatus int
		wantAudit  [][2]string
		// wantJob is whether a follow_up job is waiting afterwards.
		wantJob bool
		// wantDetail is a part of the problem's detail.
		wantDetail string
	}{
		{
			name: "a responder asks about a finished investigation", id: inv1, who: asAdmin, body: `{"text": "Was it the cache?"}`,
			wantStatus: http.StatusAccepted, wantAudit: [][2]string{{"user", adminUserID}}, wantJob: true,
		},
		{
			name: "a token asks", id: inv1, who: asToken, body: `{"text": "Was it the cache?"}`,
			wantStatus: http.StatusAccepted, wantAudit: [][2]string{{"token", adminTokenID}}, wantJob: true,
		},
		{
			name: "a question to a running investigation waits for its run", id: inv3, who: asAdmin, body: `{"text": "Check the cache too."}`,
			wantStatus: http.StatusAccepted, wantAudit: [][2]string{{"user", adminUserID}},
		},
		{name: "a viewer is denied", id: inv1, who: asViewer, body: `{"text": "Hi"}`, wantStatus: http.StatusForbidden},
		{name: "no CSRF token", id: inv1, who: asAdminWithoutCSRF, body: `{"text": "Hi"}`, wantStatus: http.StatusForbidden},
		{name: "an empty question", id: inv1, who: asAdmin, body: `{"text": "  "}`, wantStatus: http.StatusBadRequest},
		{name: "no question", id: inv1, who: asAdmin, body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "a body that is not JSON", id: inv1, who: asAdmin, body: `text`, wantStatus: http.StatusBadRequest},
		{
			name: "a question that is too long", id: inv1, who: asAdmin, body: `{"text": "` + strings.Repeat("a", 4001) + `"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "a triage-only result", id: inv2, who: asAdmin, body: `{"text": "Why?"}`,
			prepare: "UPDATE investigations SET stop_reason = 'triage' WHERE id = '" + inv2 + "'", wantStatus: http.StatusConflict,
			wantDetail: "triage-only",
		},
		{name: "a failed investigation", id: inv4, who: asAdmin, body: `{"text": "Why?"}`, wantStatus: http.StatusConflict, wantDetail: "failed"},
		{name: "no such investigation", id: uuid.NewString(), who: asAdmin, body: `{"text": "Why?"}`, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupActions(t)

			if tt.prepare != "" {
				_, err := s.pool.Exec(t.Context(), tt.prepare)
				require.NoError(t, err)
			}

			rec := s.post(t, "/api/v1/investigations/"+tt.id+"/follow-ups", tt.who, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantAudit, s.auditRows(t, investigator.ActionFollowUpAsked))

			var questions, jobs int

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM investigation_inbox WHERE investigation_id = $1 AND kind = 'follow_up' AND key IS NOT NULL AND created_at > now() - interval '1 minute'", tt.id).
				Scan(&questions))
			require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'follow_up' AND payload->>'investigation_id' = $1", tt.id).Scan(&jobs))

			if tt.wantStatus != http.StatusAccepted {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
				require.Zero(t, jobs)
				require.Contains(t, rec.Body.String(), tt.wantDetail)

				return
			}

			require.Equal(t, 1, questions)
			require.Equal(t, tt.wantJob, jobs == 1)
			require.JSONEq(t, `{"added": true}`, rec.Body.String())
		})
	}
}

func TestAskFollowUpStoresTheAuthorAndRedactsTheText(t *testing.T) {
	t.Parallel()

	s := setupActions(t)

	rec := s.post(t, "/api/v1/investigations/"+inv1+"/follow-ups", asAdmin, `{"text": "  Mail ops@example.com about it  "}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	var body []byte

	require.NoError(t, s.pool.QueryRow(t.Context(),
		"SELECT body FROM investigation_inbox WHERE investigation_id = $1 AND kind = 'follow_up' AND key IS NOT NULL AND delivered_seq IS NULL AND created_at > now() - interval '1 minute'", inv1).
		Scan(&body))

	var stored struct {
		Text     string `json:"text"`
		Author   string `json:"author"`
		AuthorID string `json:"author_id"`
	}

	require.NoError(t, json.Unmarshal(body, &stored))
	require.Equal(t, "Ad", stored.Author)
	require.Equal(t, adminUserID, stored.AuthorID)
	require.NotContains(t, stored.Text, "ops@example.com")
	require.True(t, strings.HasPrefix(stored.Text, "Mail "), stored.Text)

	var details []byte

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT details FROM audit_log WHERE action = $1", investigator.ActionFollowUpAsked).Scan(&details))
	require.NotContains(t, string(details), "ops@example.com", "the audit row does not keep the question")
}

func TestCancelInvestigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		who        requestAs
		wantStatus int
		// wantRequested is the body's requested.
		wantRequested bool
		wantAudit     [][2]string
	}{
		{name: "a responder stops a running investigation", id: inv3, who: asAdmin, wantStatus: http.StatusOK, wantRequested: true, wantAudit: [][2]string{{"user", adminUserID}}},
		{name: "a token stops a queued investigation", id: inv5, who: asToken, wantStatus: http.StatusOK, wantRequested: true, wantAudit: [][2]string{{"token", adminTokenID}}},
		{name: "an investigation that has ended", id: inv1, who: asAdmin, wantStatus: http.StatusOK},
		{name: "a viewer is denied", id: inv3, who: asViewer, wantStatus: http.StatusForbidden},
		{name: "no CSRF token", id: inv3, who: asAdminWithoutCSRF, wantStatus: http.StatusForbidden},
		{name: "no such investigation", id: uuid.NewString(), who: asAdmin, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupActions(t)
			path := "/api/v1/investigations/" + tt.id + "/cancel"

			rec := s.post(t, path, tt.who, "")
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantAudit, s.auditRows(t, investigator.ActionCancelRequested))

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			var got struct {
				Requested bool `json:"requested"`
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.wantRequested, got.Requested)

			if !tt.wantRequested {
				return
			}

			var by *string

			var slackUser *string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT cancel_requested_by::text, cancel_requested_by_slack_user FROM investigations WHERE id = $1", tt.id).Scan(&by, &slackUser))
			require.NotNil(t, by)
			require.Equal(t, tt.wantAudit[0][1], *by, "the user or token who asked is kept")
			require.Nil(t, slackUser)

			again := s.post(t, path, tt.who, "")
			require.Equal(t, http.StatusOK, again.Code, again.Body.String())
			require.JSONEq(t, `{"requested": false}`, again.Body.String(), "a second stop asks for nothing")
			require.Len(t, s.auditRows(t, investigator.ActionCancelRequested), 1)
		})
	}
}

func TestResolveInvestigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		who        requestAs
		wantStatus int
		// wantResolved is the body's resolved.
		wantResolved bool
		wantAudit    [][2]string
	}{
		{name: "a responder resolves an open group", id: inv1, who: asAdmin, wantStatus: http.StatusOK, wantResolved: true, wantAudit: [][2]string{{"user", adminUserID}}},
		{name: "a token resolves an open group", id: inv3, who: asToken, wantStatus: http.StatusOK, wantResolved: true, wantAudit: [][2]string{{"token", adminTokenID}}},
		{name: "a group that closed with its window", id: inv2, who: asAdmin, wantStatus: http.StatusConflict},
		{name: "a viewer is denied", id: inv1, who: asViewer, wantStatus: http.StatusForbidden},
		{name: "no CSRF token", id: inv1, who: asAdminWithoutCSRF, wantStatus: http.StatusForbidden},
		{name: "no such investigation", id: uuid.NewString(), who: asAdmin, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupActions(t)
			path := "/api/v1/investigations/" + tt.id + "/resolve"

			rec := s.post(t, path, tt.who, "")
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantAudit, s.auditRows(t, grouper.ActionResolved))

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				return
			}

			require.JSONEq(t, `{"resolved": true}`, rec.Body.String())

			var status, reason string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT g.status, g.closed_reason FROM event_groups g JOIN investigations i ON i.group_id = g.id WHERE i.id = $1", tt.id).Scan(&status, &reason))
			require.Equal(t, "closed", status)
			require.Equal(t, grouper.ClosedResolved, reason)

			var running int

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM watchers w JOIN investigations i ON i.group_id = w.group_id WHERE i.id = $1 AND w.status = 'running'", tt.id).Scan(&running))
			require.Zero(t, running, "the group's watchers have ended")

			again := s.post(t, path, tt.who, "")
			require.Equal(t, http.StatusOK, again.Code, again.Body.String())
			require.JSONEq(t, `{"resolved": false}`, again.Body.String(), "resolving twice changes nothing")
			require.Len(t, s.auditRows(t, grouper.ActionResolved), 1)
		})
	}
}

func TestStopWatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		id         string
		who        requestAs
		wantStatus int
		// wantStopped is the body's stopped.
		wantStopped bool
		wantAudit   [][2]string
	}{
		{name: "a responder stops a running watcher", id: watcherRunning, who: asAdmin, wantStatus: http.StatusOK, wantStopped: true, wantAudit: [][2]string{{"user", adminUserID}}},
		{name: "a token stops a running watcher", id: watcherRunning, who: asToken, wantStatus: http.StatusOK, wantStopped: true, wantAudit: [][2]string{{"token", adminTokenID}}},
		{name: "a watcher that has ended", id: watcherRecovered, who: asAdmin, wantStatus: http.StatusOK},
		{name: "a viewer is denied", id: watcherRunning, who: asViewer, wantStatus: http.StatusForbidden},
		{name: "no CSRF token", id: watcherRunning, who: asAdminWithoutCSRF, wantStatus: http.StatusForbidden},
		{name: "no such watcher", id: uuid.NewString(), who: asAdmin, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupActions(t)

			rec := s.post(t, "/api/v1/watchers/"+tt.id+"/stop", tt.who, "")
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var got struct {
				Stopped bool `json:"stopped"`
			}

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))
				require.Empty(t, s.auditRows(t, "watcher.stopped"))

				return
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.wantStopped, got.Stopped)
			require.Equal(t, tt.wantAudit, s.auditRows(t, "watcher.stopped"))

			if !tt.wantStopped {
				return
			}

			var status, reason, by string

			require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT status, stop_reason, stopped_by::text FROM watchers WHERE id = $1", tt.id).Scan(&status, &reason, &by))
			require.Equal(t, "stopped", status)
			require.Equal(t, "responder", reason)
			require.Equal(t, tt.wantAudit[0][1], by)

			list := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv3+"/watchers", viSession, "", nil)
			require.Equal(t, http.StatusOK, list.Code, list.Body.String())

			var watchers struct {
				Watchers []struct {
					StoppedBy struct {
						UserID string `json:"userId"`
						Name   string `json:"name"`
					} `json:"stoppedBy"`
				} `json:"watchers"`
			}

			require.NoError(t, json.Unmarshal(list.Body.Bytes(), &watchers))
			require.Len(t, watchers.Watchers, 1)
			require.Equal(t, tt.wantAudit[0][1], watchers.Watchers[0].StoppedBy.UserID)
			require.NotEmpty(t, watchers.Watchers[0].StoppedBy.Name, "the watcher API names the web user or the token")
		})
	}
}

func TestStopWatcherWithoutAStopper(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	rec := s.post(t, "/api/v1/watchers/"+watcherRunning+"/stop", asAdmin, "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestInvestigationShowsItsGroupStatus(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	tests := []struct {
		id         string
		wantStatus string
	}{
		{id: inv1, wantStatus: "open"},
		{id: inv2, wantStatus: "closed"},
	}

	for _, tt := range tests {
		rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+tt.id, viSession, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var got struct {
			GroupStatus string `json:"groupStatus"`
		}

		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		require.Equal(t, tt.wantStatus, got.GroupStatus)
	}
}

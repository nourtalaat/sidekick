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
	"github.com/nourtalaat/sidekick/internal/lessons"
)

// The lessons of fixture/lessons.sql.
const (
	lessonWaiting  = "0192d3a4-0000-7000-8000-0000000000d1"
	lessonGlobal   = "0192d3a4-0000-7000-8000-0000000000d2"
	lessonSlack    = "0192d3a4-0000-7000-8000-0000000000d3"
	lessonDisabled = "0192d3a4-0000-7000-8000-0000000000d4"
	lessonRejected = "0192d3a4-0000-7000-8000-0000000000d5"
	feedbackF9     = "0192d3a4-0000-7000-8000-0000000000f9"
	missingLesson  = "0192d3a4-0000-7000-8000-0000000000dd"
)

// lessonBody is a lesson as the API returns it.
type lessonBody struct {
	ID       string            `json:"id"`
	Text     string            `json:"text"`
	Scope    map[string]string `json:"scope"`
	Status   string            `json:"status"`
	Origin   string            `json:"origin"`
	Feedback []struct {
		FeedbackID      string `json:"feedbackId"`
		InvestigationID string `json:"investigationId"`
	} `json:"feedback"`
	CreatedBy  *string `json:"createdBy"`
	ApprovedBy *string `json:"approvedBy"`
	Helpful    int     `json:"helpful"`
	Unhelpful  int     `json:"unhelpful"`
	Version    int     `json:"version"`
}

// lessonListBody is the body of GET /lessons.
type lessonListBody struct {
	Lessons    []lessonBody   `json:"lessons"`
	NextCursor *string        `json:"nextCursor"`
	Counts     map[string]int `json:"counts"`
}

// setupLessons returns a server with the investigations and the lessons
// fixtures loaded, and the lesson store set up.
func setupLessons(t *testing.T) server {
	t.Helper()

	return setupWith(t, func(o *api.Options) { o.Lessons = lessons.NewStore(o.Pool, o.Redactor) }, "fixture/rules_api.sql", "fixture/investigations.sql", "fixture/lessons.sql")
}

func lessonIDsOf(l lessonListBody) []string {
	out := make([]string, len(l.Lessons))
	for i, x := range l.Lessons {
		out[i] = x.ID
	}

	return out
}

func decodeLessonList(t *testing.T, rec *httptest.ResponseRecorder) lessonListBody {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var l lessonListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l))

	return l
}

func decodeLesson(t *testing.T, rec *httptest.ResponseRecorder, status int) lessonBody {
	t.Helper()

	require.Equal(t, status, rec.Code, rec.Body.String())

	var l lessonBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l))

	return l
}

func TestListLessons(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	tests := []struct {
		name  string
		query string
		// want are the IDs of the lessons found, newest first.
		want []string
	}{
		{name: "every lesson, newest first", want: []string{lessonRejected, lessonDisabled, lessonSlack, lessonGlobal, lessonWaiting}},
		{name: "waiting for review", query: "?status=proposed", want: []string{lessonWaiting}},
		{name: "active", query: "?status=active", want: []string{lessonSlack, lessonGlobal}},
		{name: "one service", query: "?service=checkout-api", want: []string{lessonSlack, lessonWaiting}},
		{name: "words in the text", query: "?q=" + url.QueryEscape("deploys"), want: []string{lessonSlack}},
		{name: "words and a status that exclude each other", query: "?q=deploys&status=rejected"},
		{name: "a page of two", query: "?limit=2", want: []string{lessonRejected, lessonDisabled}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			list := decodeLessonList(t, s.do(t, http.MethodGet, "/api/v1/lessons"+tt.query, viSession, viCSRF, nil))
			require.Equal(t, tt.want, append([]string(nil), lessonIDsOf(list)...))
			require.Equal(t, map[string]int{"proposed": 1, "active": 2, "disabled": 1, "rejected": 1}, list.Counts, "counts ignore the filters")
		})
	}

	t.Run("the fields of a lesson", func(t *testing.T) {
		t.Parallel()

		list := decodeLessonList(t, s.do(t, http.MethodGet, "/api/v1/lessons?status=proposed", viSession, viCSRF, nil))
		require.Len(t, list.Lessons, 1)

		l := list.Lessons[0]
		require.Equal(t, "Search the service the alert names, not the route.", l.Text)
		require.Equal(t, map[string]string{"service": "checkout-api"}, l.Scope)
		require.Equal(t, "feedback", l.Origin)
		require.Equal(t, 1, l.Version)
		require.Len(t, l.Feedback, 1)
		require.Equal(t, feedbackF9, l.Feedback[0].FeedbackID)
		require.Equal(t, inv2, l.Feedback[0].InvestigationID)
		require.Nil(t, l.CreatedBy)
		require.Nil(t, l.ApprovedBy)

		global := decodeLessonList(t, s.do(t, http.MethodGet, "/api/v1/lessons?status=active", viSession, viCSRF, nil)).Lessons[1]
		require.Equal(t, lessonGlobal, global.ID)
		require.Empty(t, global.Scope)
		require.Equal(t, 3, global.Helpful)
		require.Equal(t, 1, global.Unhelpful)
		require.NotNil(t, global.CreatedBy)
	})

	t.Run("pages follow each other", func(t *testing.T) {
		t.Parallel()

		var (
			got  []string
			path = "/api/v1/lessons?limit=2"
		)

		for range 5 {
			list := decodeLessonList(t, s.do(t, http.MethodGet, path, viSession, viCSRF, nil))
			got = append(got, lessonIDsOf(list)...)

			if list.NextCursor == nil {
				break
			}

			path = "/api/v1/lessons?limit=2&cursor=" + url.QueryEscape(*list.NextCursor)
		}

		require.Equal(t, []string{lessonRejected, lessonDisabled, lessonSlack, lessonGlobal, lessonWaiting}, got)
	})

	for _, tt := range []struct{ name, query string }{
		{name: "a limit of zero", query: "?limit=0"},
		{name: "a limit that is too big", query: "?limit=201"},
		{name: "a cursor that is not one", query: "?cursor=abc"},
		{name: "a status that does not exist", query: "?status=archived"},
		{name: "a query that is too long", query: "?q=" + strings.Repeat("a", 201)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/lessons"+tt.query, viSession, viCSRF, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

func TestLessonRoles(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	writes := []struct {
		name         string
		method, path string
		body         any
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/lessons", body: map[string]any{"text": "A lesson."}},
		{name: "update", method: http.MethodPut, path: "/api/v1/lessons/" + lessonSlack, body: map[string]any{"version": 1, "text": "A lesson."}},
		{name: "approve", method: http.MethodPost, path: "/api/v1/lessons/" + lessonWaiting + "/approve", body: map[string]any{"version": 1}},
		{name: "delete", method: http.MethodDelete, path: "/api/v1/lessons/" + lessonSlack},
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

	t.Run("a viewer token reads", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, viewerTokenCaller, http.MethodGet, "/api/v1/lessons", nil)
		require.Len(t, decodeLessonList(t, rec).Lessons, 5)
	})

	t.Run("nobody signed in", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/lessons", "", "", nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("a write without the CSRF token", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodDelete, "/api/v1/lessons/"+lessonSlack, adSession, "", nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestCreateLesson(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		caller     ruleCaller
		body       map[string]any
		wantStatus string
		wantScope  map[string]string
		wantText   string
		// wantProblem is the field of a 422, empty for a lesson created.
		wantProblem string
	}{
		{
			name: "a global lesson is active", caller: adminCaller, body: map[string]any{"text": " Prefer short answers. "},
			wantStatus: "active", wantScope: map[string]string{}, wantText: "Prefer short answers.",
		},
		{
			name: "a scoped lesson that waits for review", caller: adminCaller,
			body:       map[string]any{"text": "Check the pool.", "scope": map[string]any{"service": "db", "route": "payments"}, "status": "proposed"},
			wantStatus: "proposed", wantScope: map[string]string{"service": "db", "route": "payments"}, wantText: "Check the pool.",
		},
		{
			name: "a token of an admin", caller: adminTokenCaller, body: map[string]any{"text": "Prefer short answers."},
			wantStatus: "active", wantScope: map[string]string{}, wantText: "Prefer short answers.",
		},
		{name: "no text", caller: adminCaller, body: map[string]any{"text": "  "}, wantProblem: "text"},
		{name: "text of 401 characters", caller: adminCaller, body: map[string]any{"text": strings.Repeat("a", 401)}, wantProblem: "text"},
		{
			name: "a scope value that is too long", caller: adminCaller,
			body:        map[string]any{"text": "Check.", "scope": map[string]any{"route": strings.Repeat("r", 201)}},
			wantProblem: "scope.route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupLessons(t)

			rec := s.sendRule(t, tt.caller, http.MethodPost, "/api/v1/lessons", tt.body)

			if tt.wantProblem != "" {
				p := ruleProblemOf(t, rec)
				require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
				require.Equal(t, "validation", p.Code)
				require.Len(t, p.Errors, 1)
				require.Equal(t, tt.wantProblem, p.Errors[0].Field)

				return
			}

			l := decodeLesson(t, rec, http.StatusCreated)
			require.Equal(t, tt.wantStatus, l.Status)
			require.Equal(t, tt.wantText, l.Text)
			require.Equal(t, "ui", l.Origin)
			require.Equal(t, 1, l.Version)

			if len(tt.wantScope) == 0 {
				require.Empty(t, l.Scope)
			} else {
				require.Equal(t, tt.wantScope, l.Scope)
			}

			if tt.caller.token == "" {
				require.NotNil(t, l.CreatedBy, "a user is recorded as the writer")
			} else {
				require.Nil(t, l.CreatedBy, "a token is not a user")
			}

			var actor string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT actor_kind FROM audit_log WHERE action = 'lesson.created' AND target_id = $1", l.ID).Scan(&actor))
			require.Equal(t, map[bool]string{true: "token", false: "user"}[tt.caller.token != ""], actor)
		})
	}
}

func TestLessonTextIsRedacted(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	// The test server redacts email addresses, as sidekick serve does with
	// the rules of the config.
	const address = "ana@example.com"

	created := decodeLesson(t, s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/lessons", map[string]any{
		"text": "Ask " + address + " about the deploy.",
	}), http.StatusCreated)
	require.NotContains(t, created.Text, address)
	require.Contains(t, created.Text, "[REDACTED:")

	updated := decodeLesson(t, s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+created.ID, map[string]any{
		"version": created.Version, "text": "Mail " + address + " first.",
	}), http.StatusOK)
	require.NotContains(t, updated.Text, address)
	require.Contains(t, updated.Text, "[REDACTED:")

	var stored string

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT text FROM lessons WHERE id = $1", created.ID).Scan(&stored))
	require.Equal(t, updated.Text, stored)

	var audited int

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE target_kind = 'lesson' AND details::text LIKE '%' || $1 || '%'", address).Scan(&audited))
	require.Zero(t, audited, "the audit log never holds the address")
}

func TestUpdateLesson(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	t.Run("an edit raises the version and is audited", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+lessonWaiting, map[string]any{
			"version": 1, "text": "Search the service the alert names.", "scope": map[string]any{"service": "checkout-api", "route": "payments"},
		})

		l := decodeLesson(t, rec, http.StatusOK)
		require.Equal(t, "Search the service the alert names.", l.Text)
		require.Equal(t, map[string]string{"service": "checkout-api", "route": "payments"}, l.Scope)
		require.Equal(t, "proposed", l.Status)
		require.Equal(t, 2, l.Version)

		var n int

		require.NoError(t, s.pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_log WHERE action = 'lesson.updated' AND target_id = $1", lessonWaiting).Scan(&n))
		require.Equal(t, 1, n)
	})

	t.Run("a lesson that waits is rejected", func(t *testing.T) {
		t.Parallel()

		other := setupLessons(t)

		rec := other.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+lessonWaiting, map[string]any{
			"version": 1, "text": "Search the service.", "scope": map[string]any{"service": "checkout-api"}, "status": "rejected",
		})
		require.Equal(t, "rejected", decodeLesson(t, rec, http.StatusOK).Status)
	})

	t.Run("an active lesson is disabled, then enabled", func(t *testing.T) {
		t.Parallel()

		other := setupLessons(t)

		first := other.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+lessonSlack, map[string]any{
			"version": 1, "text": "Look at deploys.", "scope": map[string]any{"service": "checkout-api"}, "status": "disabled",
		})
		require.Equal(t, "disabled", decodeLesson(t, first, http.StatusOK).Status)

		second := other.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+lessonSlack, map[string]any{
			"version": 2, "text": "Look at deploys.", "scope": map[string]any{"service": "checkout-api"}, "status": "active",
		})
		require.Equal(t, "active", decodeLesson(t, second, http.StatusOK).Status)
	})

	tests := []struct {
		name       string
		id         string
		body       map[string]any
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{
			name: "a version that is out of date", id: lessonSlack, wantStatus: http.StatusConflict, wantCode: "stale",
			body: map[string]any{"version": 7, "text": "Look at deploys.", "scope": map[string]any{"service": "checkout-api"}},
		},
		{
			name: "a lesson that does not exist", id: missingLesson, wantStatus: http.StatusNotFound, wantCode: "not_found",
			body: map[string]any{"version": 1, "text": "Look at deploys.", "scope": map[string]any{"service": "checkout-api"}},
		},
		{
			name: "a lesson from feedback must keep a scope", id: lessonWaiting, wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "scope",
			body: map[string]any{"version": 1, "text": "No scope."},
		},
		{
			name: "approving through an edit", id: lessonWaiting, wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "status",
			body: map[string]any{"version": 1, "text": "Search.", "scope": map[string]any{"service": "checkout-api"}, "status": "active"},
		},
		{
			name: "text that is too long", id: lessonSlack, wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "text",
			body: map[string]any{"version": 1, "text": strings.Repeat("a", 401), "scope": map[string]any{"service": "checkout-api"}},
		},
		{name: "no version", id: lessonSlack, wantStatus: http.StatusBadRequest, body: map[string]any{"text": "Look at deploys."}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, adminCaller, http.MethodPut, "/api/v1/lessons/"+tt.id, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantCode == "" {
				return
			}

			p := ruleProblemOf(t, rec)
			require.Equal(t, tt.wantCode, p.Code)

			if tt.wantField != "" {
				require.Len(t, p.Errors, 1)
				require.Equal(t, tt.wantField, p.Errors[0].Field)
			}
		})
	}
}

func TestApproveLesson(t *testing.T) {
	t.Parallel()

	t.Run("a lesson that waits becomes active, with its approver", func(t *testing.T) {
		t.Parallel()

		s := setupLessons(t)

		rec := s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/lessons/"+lessonWaiting+"/approve", map[string]any{"version": 1})

		l := decodeLesson(t, rec, http.StatusOK)
		require.Equal(t, "active", l.Status)
		require.Equal(t, 2, l.Version)
		require.NotNil(t, l.ApprovedBy)

		var n int

		require.NoError(t, s.pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_log WHERE action = 'lesson.approved' AND target_id = $1", lessonWaiting).Scan(&n))
		require.Equal(t, 1, n)

		// Approving it again is refused.
		again := s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/lessons/"+lessonWaiting+"/approve", map[string]any{"version": 2})
		require.Equal(t, http.StatusConflict, again.Code)
		require.Equal(t, "not_proposed", ruleProblemOf(t, again).Code)
	})

	tests := []struct {
		name       string
		id         string
		version    int
		wantStatus int
		wantCode   string
	}{
		{name: "a version that is out of date", id: lessonWaiting, version: 4, wantStatus: http.StatusConflict, wantCode: "stale"},
		{name: "an active lesson", id: lessonSlack, version: 1, wantStatus: http.StatusConflict, wantCode: "not_proposed"},
		{name: "a rejected lesson", id: lessonRejected, version: 1, wantStatus: http.StatusConflict, wantCode: "not_proposed"},
		{name: "a lesson that does not exist", id: missingLesson, version: 1, wantStatus: http.StatusNotFound, wantCode: "not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupLessons(t)

			rec := s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/lessons/"+tt.id+"/approve", map[string]any{"version": tt.version})
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantCode, ruleProblemOf(t, rec).Code)
		})
	}
}

func TestDeleteLesson(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	rec := s.sendRule(t, adminCaller, http.MethodDelete, "/api/v1/lessons/"+lessonDisabled, nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	list := decodeLessonList(t, s.do(t, http.MethodGet, "/api/v1/lessons", viSession, viCSRF, nil))
	require.Len(t, list.Lessons, 4)

	var text string

	require.NoError(t, s.pool.QueryRow(t.Context(),
		"SELECT details->'old'->>'text' FROM audit_log WHERE action = 'lesson.deleted' AND target_id = $1", lessonDisabled).Scan(&text))
	require.Equal(t, "Disk alerts on db are often the nightly backup.", text)

	// The investigation that used it no longer lists it.
	detail := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv1, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, detail.Code)

	var inv struct {
		Lessons []struct {
			ID string `json:"id"`
		} `json:"lessons"`
	}

	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &inv))
	require.Len(t, inv.Lessons, 2)

	again := s.sendRule(t, adminCaller, http.MethodDelete, "/api/v1/lessons/"+lessonDisabled, nil)
	require.Equal(t, http.StatusNotFound, again.Code)
}

func TestInvestigationShowsItsLessons(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	var inv struct {
		Lessons []struct {
			ID     string            `json:"id"`
			Text   string            `json:"text"`
			Scope  map[string]string `json:"scope"`
			Status string            `json:"status"`
		} `json:"lessons"`
	}

	detail := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv1, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	require.NoError(t, json.Unmarshal(detail.Body.Bytes(), &inv))

	// In the order they were written, with the status they have now.
	require.Len(t, inv.Lessons, 3)
	require.Equal(t, lessonGlobal, inv.Lessons[0].ID)
	require.Equal(t, "Say what you could not check.", inv.Lessons[0].Text)
	require.Empty(t, inv.Lessons[0].Scope)
	require.Equal(t, lessonSlack, inv.Lessons[1].ID)
	require.Equal(t, map[string]string{"service": "checkout-api", "alert": "CheckoutLatency"}, inv.Lessons[1].Scope)
	require.Equal(t, lessonDisabled, inv.Lessons[2].ID)
	require.Equal(t, "disabled", inv.Lessons[2].Status)

	none := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv2, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, none.Code)
	require.Contains(t, none.Body.String(), `"lessons":[]`)
}

func TestStepsShowTheLessonsOfTheirPrompt(t *testing.T) {
	t.Parallel()

	s := setupLessons(t)

	rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+inv1+"/steps", viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var steps struct {
		Steps []struct {
			Kind    string `json:"kind"`
			Lessons *struct {
				Used    []string `json:"used"`
				LeftOut []struct {
					ID     string            `json:"id"`
					Reason string            `json:"reason"`
					Scope  map[string]string `json:"scope"`
				} `json:"leftOut"`
				LeftOutTotal int `json:"leftOutTotal"`
				Candidates   int `json:"candidates"`
			} `json:"lessons"`
		} `json:"steps"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &steps))
	require.Len(t, steps.Steps, 1)
	require.Equal(t, "prompt", steps.Steps[0].Kind)

	l := steps.Steps[0].Lessons
	require.NotNil(t, l)
	require.Equal(t, []string{lessonGlobal, lessonSlack}, l.Used)
	require.Len(t, l.LeftOut, 1)
	require.Equal(t, lessonDisabled, l.LeftOut[0].ID)
	require.Equal(t, "max_tokens", l.LeftOut[0].Reason)
	require.Equal(t, map[string]string{"service": "db"}, l.LeftOut[0].Scope)
	require.Equal(t, 1, l.LeftOutTotal)
	require.Equal(t, 3, l.Candidates)
}

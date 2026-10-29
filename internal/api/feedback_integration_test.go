//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The sessions of fixture/feedback_users.sql, as cookie values, and their
// CSRF tokens.
var (
	faSession = sessionValue(0x06)
	fbSession = sessionValue(0x07)
)

const (
	faCSRF = "csrf-fa"
	fbCSRF = "csrf-fb"
)

// The users of fixture/feedback_users.sql that give feedback.
const (
	userFa = "01920000-0000-7000-8000-000000000060"
	userFb = "01920000-0000-7000-8000-000000000061"
)

// feedbackBody is the response of POST /investigations/{id}/feedback.
type feedbackBody struct {
	ID              string  `json:"id"`
	InvestigationID string  `json:"investigationId"`
	TargetKind      string  `json:"targetKind"`
	TargetRef       string  `json:"targetRef"`
	Rating          string  `json:"rating"`
	Reason          *string `json:"reason"`
	CorrectVerdict  *string `json:"correctVerdict"`
	CorrectCause    *string `json:"correctCause"`
	Channel         string  `json:"channel"`
	Confirmation    string  `json:"confirmation"`
}

func TestCreateInvestigationFeedback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		session string
		csrf    string
		body    string
		// wantStatus is the status of the answer; a problem is checked
		// against wantDetail.
		wantStatus int
		wantDetail string
		// want is the answer of a stored feedback.
		want feedbackBody
	}{
		{
			name: "a responder rates a finding", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"finding","targetRef":"run:2/finding:f2","rating":"wrong","reason":"No deploy at that time."}`,
			wantStatus: http.StatusCreated,
			want: feedbackBody{
				InvestigationID: inv1, TargetKind: "finding", TargetRef: "run:2/finding:f2", Rating: "wrong",
				Reason: new("No deploy at that time."), Channel: "ui", Confirmation: "unchanged",
			},
		},
		{
			name: "a responder rates a fix", id: inv1, session: fbSession, csrf: fbCSRF,
			body:       `{"targetKind":"fix","targetRef":"run:2/fix:x1","rating":"correct"}`,
			wantStatus: http.StatusCreated,
			want:       feedbackBody{InvestigationID: inv1, TargetKind: "fix", TargetRef: "run:2/fix:x1", Rating: "correct", Channel: "ui", Confirmation: "unchanged"},
		},
		{
			name: "an admin includes the responder role", id: inv1, session: adSession, csrf: adCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`,
			wantStatus: http.StatusCreated,
			want:       feedbackBody{InvestigationID: inv1, TargetKind: "verdict", TargetRef: "run:2/verdict", Rating: "correct", Channel: "ui", Confirmation: "unchanged"},
		},
		{
			name: "a tool call of a running investigation", id: inv3, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"tool_call","targetRef":"c1","rating":"wrong"}`,
			wantStatus: http.StatusCreated,
			want:       feedbackBody{InvestigationID: inv3, TargetKind: "tool_call", TargetRef: "c1", Rating: "wrong", Channel: "ui", Confirmation: "unchanged"},
		},
		{
			name: "a Correct on the verdict confirms it", id: inv2, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:1/verdict","rating":"correct"}`,
			wantStatus: http.StatusCreated,
			want:       feedbackBody{InvestigationID: inv2, TargetKind: "verdict", TargetRef: "run:1/verdict", Rating: "correct", Channel: "ui", Confirmation: "confirmed"},
		},
		{
			name: "a viewer is denied", id: inv1, session: viSession, csrf: viCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`,
			wantStatus: http.StatusForbidden, wantDetail: "This needs the responder role.",
		},
		{
			name: "no session", id: inv1,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`,
			wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token.",
		},
		{
			name: "no CSRF token", id: inv1, session: faSession,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`,
			wantStatus: http.StatusForbidden, wantDetail: "Send the session's CSRF token in the X-CSRF-Token header.",
		},
		{
			name: "an investigation that does not exist", id: "0192d3a4-0000-7000-8000-0000000000ff", session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"tool_call","targetRef":"c1","rating":"wrong"}`,
			wantStatus: http.StatusNotFound, wantDetail: "No investigation has this ID.",
		},
		{
			name: "the verdict of a running investigation", id: inv3, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"wrong"}`,
			wantStatus: http.StatusConflict, wantDetail: "The investigation has no verdict yet; only its tool calls can be rated.",
		},
		{
			name: "a verdict of an older run", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:1/verdict","rating":"wrong"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "This part is not in the investigation's current report or steps.",
		},
		{
			name: "a finding that is not in the report", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"finding","targetRef":"run:2/finding:f9","rating":"wrong"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "This part is not in the investigation's current report or steps.",
		},
		{
			name: "a fix named like a finding", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"fix","targetRef":"run:2/fix:f1","rating":"wrong"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "This part is not in the investigation's current report or steps.",
		},
		{
			name: "a tool call that is not in the steps", id: inv3, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"tool_call","targetRef":"c9","rating":"wrong"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "This part is not in the investigation's current report or steps.",
		},
		{
			name: "an investigation with no report", id: inv2, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"finding","targetRef":"run:1/finding:f1","rating":"wrong"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "This part is not in the investigation's current report or steps.",
		},
		{
			name: "a real verdict that is not a verdict", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"wrong","correctVerdict":"maybe"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "The real verdict is not a verdict, or it contradicts the rating of the verdict.",
		},
		{
			name: "Correct with another real verdict", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct","correctVerdict":"false_positive"}`,
			wantStatus: http.StatusUnprocessableEntity, wantDetail: "The real verdict is not a verdict, or it contradicts the rating of the verdict.",
		},
		{
			name: "an unknown rating", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"maybe"}`,
			wantStatus: http.StatusBadRequest, wantDetail: "The rating must be correct or wrong.",
		},
		{
			name: "an unknown target kind", id: inv1, session: faSession, csrf: faCSRF,
			body:       `{"targetKind":"investigation","targetRef":"run:2/verdict","rating":"correct"}`,
			wantStatus: http.StatusBadRequest, wantDetail: "The target kind must be verdict, finding, fix or tool_call.",
		},
		{
			name: "a reason that is too long", id: inv1, session: faSession, csrf: faCSRF,
			body:       fmt.Sprintf(`{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"wrong","reason":%q}`, strings.Repeat("x", feedback.MaxReasonChars+1)),
			wantStatus: http.StatusBadRequest, wantDetail: "The reason is longer than 2000 characters.",
		},
		{
			name: "a body that is not JSON", id: inv1, session: faSession, csrf: faCSRF,
			body: `{`, wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupFeedback(t)

			rec := postFeedback(t, s, tt.id, tt.session, tt.csrf, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusCreated {
				require.Equal(t, "application/problem+json", rec.Header().Get("Content-Type"))

				if tt.wantDetail != "" {
					require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, "/api/v1/investigations/"+tt.id+"/feedback"), rec.Body.String())
				}

				require.Zero(t, countFeedback(t, s.pool, "channel = 'ui'"), "nothing is stored")

				return
			}

			var got feedbackBody

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.NotEmpty(t, got.ID)

			got.ID = ""
			require.Equal(t, tt.want, got)
			require.Equal(t, 1, countFeedback(t, s.pool, "channel = 'ui'"))
		})
	}
}

func TestCreateInvestigationFeedbackRefusesAPITokens(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	_, err := s.pool.Exec(t.Context(),
		`INSERT INTO api_tokens (id, name, token_hash, role, created_by, created_at, expires_at)
		 VALUES ('01920000-0000-7000-8000-000000000043', 'responder bot', sha256(convert_to('skt_responder', 'UTF8')), 'responder',
		         '01920000-0000-7000-8000-000000000040', now(), now() + interval '30 days')`)
	require.NoError(t, err)

	rec := s.doWithToken(t, http.MethodPost, "/api/v1/investigations/"+inv1+"/feedback", "skt_responder",
		strings.NewReader(`{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`))
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.JSONEq(t,
		problem(http.StatusForbidden, "Feedback needs a signed-in user, not an API token.", "/api/v1/investigations/"+inv1+"/feedback"),
		rec.Body.String())
	require.Zero(t, countFeedback(t, s.pool, "channel = 'ui'"))
}

func TestCreateInvestigationFeedbackWithARealVerdict(t *testing.T) {
	t.Parallel()

	s := setupFeedbackWith(t, func(o *api.Options) { o.Memory = memory.SyncOptions{Embed: true} })

	// inv1 is confirmed as a true positive with the cause Deploy.
	first := postFeedback(t, s, inv1, faSession, faCSRF,
		`{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"wrong","reason":"It was a load test.","correctVerdict":"false_positive","correctCause":"A load test."}`)
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	var got feedbackBody

	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &got))
	require.Equal(t, "revised", got.Confirmation)
	require.Equal(t, new("false_positive"), got.CorrectVerdict)
	require.Equal(t, new("A load test."), got.CorrectCause)

	confirmed := confirmation(t, s.pool, inv1)
	require.Equal(t, confirmedRow{Verdict: "false_positive", Cause: "A load test.", By: userFa}, confirmed)
	require.Equal(t, []string{"false_positive"}, memoryVerdicts(t, s.pool, inv1))
	require.Equal(t, 1, embedJobs(t, s.pool), "the memory entry is embedded when vector memory is on")

	// A later submission that disagrees revises it again, and is audited.
	second := postFeedback(t, s, inv1, fbSession, fbCSRF, `{"targetKind":"verdict","targetRef":"run:2/verdict","rating":"correct"}`)
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())

	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &got))
	require.Equal(t, "revised", got.Confirmation)
	require.Equal(t, confirmedRow{Verdict: "true_positive", Cause: "Deploy 4f2c1a", By: userFb}, confirmation(t, s.pool, inv1))

	require.Equal(t, []auditRow{
		{Actor: "user " + userFa, Action: "verdict.revised", Old: "true_positive", New: "false_positive"},
		{Actor: "user " + userFb, Action: "verdict.revised", Old: "false_positive", New: "true_positive"},
	}, auditRows(t, s.pool, inv1))
}

func TestCreateInvestigationFeedbackConfirmsFirstWithARealVerdict(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	// inv2 is not confirmed. A Wrong with the real verdict confirms it and
	// is audited; a plain Correct on a verdict would not be audited.
	rec := postFeedback(t, s, inv2, faSession, faCSRF,
		`{"targetKind":"verdict","targetRef":"run:1/verdict","rating":"wrong","correctVerdict":"true_positive","correctCause":"Disk filled up."}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Equal(t, confirmedRow{Verdict: "true_positive", Cause: "Disk filled up.", By: userFa}, confirmation(t, s.pool, inv2))
	require.Equal(t, []string{"true_positive"}, memoryVerdicts(t, s.pool, inv2))
	require.Equal(t, []auditRow{{Actor: "user " + userFa, Action: "verdict.confirmed", New: "true_positive"}}, auditRows(t, s.pool, inv2))
	require.Zero(t, embedJobs(t, s.pool), "no embedding without vector memory")
}

// wantFeedbackMetrics is the sidekick_feedback_total of the four
// submissions of TestCreateInvestigationFeedbackKeepsOneOpinionPerUser.
const wantFeedbackMetrics = `# HELP sidekick_feedback_total Ratings responders stored, by target kind and rating. A repeated click is not counted.
# TYPE sidekick_feedback_total counter
sidekick_feedback_total{rating="correct",target="finding"} 1
sidekick_feedback_total{rating="correct",target="verdict"} 0
sidekick_feedback_total{rating="wrong",target="finding"} 2
sidekick_feedback_total{rating="wrong",target="verdict"} 0
`

func TestCreateInvestigationFeedbackRedactsTheFreeText(t *testing.T) {
	t.Parallel()

	s := setupFeedback(t)

	rec := postFeedback(t, s, inv2, faSession, faCSRF,
		`{"targetKind":"verdict","targetRef":"run:1/verdict","rating":"wrong","reason":"Ask ana@example.com about it.","correctVerdict":"true_positive","correctCause":"Run by bob@example.com."}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var got feedbackBody

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	var reason, cause, confirmed string

	require.NoError(t, s.pool.QueryRow(t.Context(),
		"SELECT reason, correct_cause, (SELECT confirmed_cause FROM investigations WHERE id = $1) FROM feedback WHERE id = $2", inv2, got.ID).
		Scan(&reason, &cause, &confirmed))

	for name, text := range map[string]string{"reason": reason, "real cause": cause, "confirmed cause": confirmed} {
		require.NotContains(t, text, "@example.com", name)
		require.Contains(t, text, "[REDACTED:emails:", name)
	}

	require.NotContains(t, rec.Body.String(), "@example.com", "the answer shows the redacted text")
}

func TestCreateInvestigationFeedbackKeepsOneOpinionPerUser(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	metrics, err := feedback.NewMetrics(reg)
	require.NoError(t, err)

	s := setupFeedbackWith(t, func(o *api.Options) { o.Feedback = metrics })

	const target = `"targetKind":"finding","targetRef":"run:2/finding:f1"`

	for _, send := range []struct {
		session, csrf, body string
	}{
		{faSession, faCSRF, `{` + target + `,"rating":"wrong"}`},
		{faSession, faCSRF, `{` + target + `,"rating":"wrong","reason":"Added later."}`},
		{faSession, faCSRF, `{` + target + `,"rating":"correct"}`},
		{fbSession, fbCSRF, `{` + target + `,"rating":"wrong"}`},
	} {
		rec := postFeedback(t, s, inv1, send.session, send.csrf, send.body)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}

	require.Equal(t, 2, countFeedback(t, s.pool, "channel = 'ui'"), "one row per user")
	require.Equal(t, "correct", ratingOf(t, s.pool, userFa))
	require.Equal(t, "wrong", ratingOf(t, s.pool, userFb))

	// A new reason on the same rating is not counted again.
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(wantFeedbackMetrics), feedback.MetricFeedback))
}

// setupFeedback returns a server with the investigations and the responders
// that give feedback.
func setupFeedback(t *testing.T) server {
	t.Helper()

	return setupFeedbackWith(t, nil)
}

// setupFeedbackWith is setupFeedback with tune, when not nil, changing the
// API's options.
func setupFeedbackWith(t *testing.T, tune func(*api.Options)) server {
	t.Helper()

	s := setupInvestigationsWith(t, tune)
	storetest.LoadFixture(t, s.pool, "fixture/feedback_users.sql")

	return s
}

// postFeedback sends a body to POST /investigations/{id}/feedback.
func postFeedback(t *testing.T, s server, id, session, csrf, body string) *httptest.ResponseRecorder {
	t.Helper()

	return s.do(t, http.MethodPost, "/api/v1/investigations/"+id+"/feedback", session, csrf, strings.NewReader(body))
}

// countFeedback counts the feedback rows that match where.
func countFeedback(t *testing.T, pool *pgxpool.Pool, where string) int {
	t.Helper()

	var n int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM feedback WHERE "+where).Scan(&n))

	return n
}

// ratingOf returns the rating the user gave the finding of inv1.
func ratingOf(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()

	var rating string

	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT rating FROM feedback WHERE user_id = $1 AND target_ref = 'run:2/finding:f1'", userID).Scan(&rating))

	return rating
}

// confirmedRow is the confirmed verdict of an investigation.
type confirmedRow struct {
	Verdict, Cause, By string
}

// confirmation returns who confirmed the investigation with id, and what.
func confirmation(t *testing.T, pool *pgxpool.Pool, id string) confirmedRow {
	t.Helper()

	var row confirmedRow

	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT coalesce(confirmed_verdict, ''), coalesce(confirmed_cause, ''), coalesce(confirmed_by::text, '') FROM investigations WHERE id = $1", id).
		Scan(&row.Verdict, &row.Cause, &row.By))

	return row
}

// memoryVerdicts returns the verdicts of the memory entries of the
// investigation with id.
func memoryVerdicts(t *testing.T, pool *pgxpool.Pool, id string) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT verdict FROM memory_entries WHERE investigation_id = $1", id)
	require.NoError(t, err)

	verdicts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return verdicts
}

// embedJobs counts the jobs that embed a memory entry.
func embedJobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var n int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindEmbedMemory).Scan(&n))

	return n
}

// auditRow is an audit log row of a verdict change, as the tests compare it.
type auditRow struct {
	Actor, Action, Old, New string
}

// auditRows returns the audit rows of the investigation with id, oldest
// first.
func auditRows(t *testing.T, pool *pgxpool.Pool, id string) []auditRow {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT actor_kind || ' ' || actor_id, action, coalesce(details->'old'->>'verdict', ''), coalesce(details->'new'->>'verdict', '')
		 FROM audit_log WHERE investigation_id = $1 ORDER BY id`, id)
	require.NoError(t, err)

	defer rows.Close()

	var out []auditRow

	for rows.Next() {
		var r auditRow

		require.NoError(t, rows.Scan(&r.Actor, &r.Action, &r.Old, &r.New))

		out = append(out, r)
	}

	require.NoError(t, rows.Err())

	return out
}

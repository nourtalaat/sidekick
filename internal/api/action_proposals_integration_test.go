//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	actionproposals "github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// The sessions and the token of fixture/proposals.sql, and their CSRF
// tokens.
var (
	apSession = sessionValue(0x0b)
	boSession = sessionValue(0x0c)
)

//nolint:gosec // Test tokens of the fixtures.
const (
	apCSRF        = "csrf-ap"
	boCSRF        = "csrf-bo"
	approverToken = "skt_approver"
)

// The proposals of fixture/proposals.sql.
const (
	propFirst            = "0192d3a4-0000-7000-8000-0000000000f1"
	propSecond           = "0192d3a4-0000-7000-8000-0000000000f2"
	propExpired          = "0192d3a4-0000-7000-8000-0000000000f3"
	propApproved         = "0192d3a4-0000-7000-8000-0000000000f4"
	propSucceeded        = "0192d3a4-0000-7000-8000-0000000000f5"
	propRejected         = "0192d3a4-0000-7000-8000-0000000000f6"
	propInvestgn         = "0192d3a4-0000-7000-8000-0000000000e2"
	propNotThere         = "0192d3a4-0000-7000-8000-0000000000ff"
	actionProposalsRoute = "/api/v1/proposals"
)

// actionProposalBody is a proposal as the API returns it.
type actionProposalBody struct {
	ID                 string         `json:"id"`
	InvestigationID    string         `json:"investigationId"`
	AlertTitle         string         `json:"alertTitle"`
	Route              string         `json:"route"`
	Action             string         `json:"action"`
	Params             map[string]any `json:"params"`
	ParamsText         string         `json:"paramsText"`
	Reason             string         `json:"reason"`
	Status             string         `json:"status"`
	DecidedByName      string         `json:"decidedByName"`
	DecidedBySlackUser string         `json:"decidedBySlackUser"`
	DecidedAt          string         `json:"decidedAt"`
	Error              string         `json:"error"`
	Result             *struct {
		Summary string `json:"summary"`
	} `json:"result"`
}

// actionProposalListBody is the response of GET /proposals.
type actionProposalListBody struct {
	Proposals  []actionProposalBody `json:"proposals"`
	NextCursor string               `json:"nextCursor"`
}

// apiNoteAction is an action that the API tests never run.
type apiNoteAction struct{}

func (apiNoteAction) Spec() action.Spec {
	return action.Spec{Name: "monitor-note", Description: "Writes a note.", ParamsSchema: json.RawMessage(`{"type": "object"}`)}
}

func (apiNoteAction) Validate(context.Context, json.RawMessage) error { return nil }

func (apiNoteAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

// setupActionProposals returns a server with fixture/proposals.sql and the
// proposal service, whose payments route proposes monitor-note in approved
// mode. The policy it uses is returned for tests that change it.
func setupActionProposals(t *testing.T) (server, action.StaticPolicy) {
	t.Helper()

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"monitor-note"}}}

	s := setupWith(t, func(o *api.Options) {
		svc, err := actionproposals.New(actionproposals.Options{Pool: o.Pool, Actions: map[string]action.Action{"monitor-note": apiNoteAction{}}, Policy: policy, Redactor: o.Redactor})
		require.NoError(t, err)

		o.Actions = svc
	}, "fixture/proposals.sql")

	return s, policy
}

func readActionProposals(t *testing.T, rec *httptest.ResponseRecorder) actionProposalListBody {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body actionProposalListBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func readActionProposal(t *testing.T, rec *httptest.ResponseRecorder) actionProposalBody {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body actionProposalBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	return body
}

func actionIDs(list []actionProposalBody) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.ID
	}

	return out
}

func TestListProposals(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "the proposals that wait, newest first", want: []string{propSecond, propFirst, propExpired}},
		{name: "decided proposals", query: "?status=approved&status=rejected&status=succeeded", want: []string{propApproved, propSucceeded, propRejected}},
		{name: "a status given twice is listed once", query: "?status=proposed&status=proposed", want: []string{propSecond, propFirst, propExpired}},
		{name: "one status", query: "?status=rejected", want: []string{propRejected}},
		{name: "a status nobody has", query: "?status=failed", want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+tt.query, approverToken, nil))
			require.Equal(t, tt.want, actionIDs(got.Proposals))
			require.Empty(t, got.NextCursor)
		})
	}

	// The fields of a proposal.
	got := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?status=succeeded", approverToken, nil)).Proposals[0]
	require.Equal(t, "payments", got.Route)
	require.Equal(t, "monitor-note", got.Action)
	require.Equal(t, propInvestgn, got.InvestigationID)
	require.Equal(t, "checkout-api p99 latency above 2s", got.AlertTitle)
	require.Equal(t, "Ap", got.DecidedByName)
	require.NotEmpty(t, got.DecidedAt)
	require.Equal(t, "Done", got.Params["title"])
	require.JSONEq(t, `{"board": "ops", "title": "Done"}`, got.ParamsText)
	require.Contains(t, got.ParamsText, "\n  \"title\": \"Done\"", "it is indented")
	require.Equal(t, "Wrote a note.", got.Result.Summary)

	// A newline inside a parameter is spelled out in the text the approver reads.
	waiting := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute, approverToken, nil)).Proposals[0]
	require.Contains(t, waiting.ParamsText, `"body": "line one\nline two"`)
}

func TestListProposalsPages(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	first := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?limit=2", approverToken, nil))
	require.Equal(t, []string{propSecond, propFirst}, actionIDs(first.Proposals))
	require.NotEmpty(t, first.NextCursor)

	second := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?limit=2&cursor="+first.NextCursor, approverToken, nil))
	require.Equal(t, []string{propExpired}, actionIDs(second.Proposals))
	require.Empty(t, second.NextCursor)
}

// TestListProposalsPagesAcrossStatuses pages through proposals of three
// statuses, which are read one status at a time and merged by time.
func TestListProposalsPagesAcrossStatuses(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	const query = "?status=rejected&status=approved&status=succeeded&limit=2"

	first := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+query, approverToken, nil))
	require.Equal(t, []string{propApproved, propSucceeded}, actionIDs(first.Proposals))
	require.NotEmpty(t, first.NextCursor)

	second := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+query+"&cursor="+first.NextCursor, approverToken, nil))
	require.Equal(t, []string{propRejected}, actionIDs(second.Proposals))
	require.Empty(t, second.NextCursor)
}

func TestListProposalsRefusals(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	tests := []struct {
		name       string
		session    string
		token      string
		path       string
		wantStatus int
		wantDetail string
	}{
		{name: "a viewer", token: "skt_viewer", path: actionProposalsRoute, wantStatus: http.StatusForbidden, wantDetail: "This needs the approver role."},
		{name: "no sign-in", path: actionProposalsRoute, wantStatus: http.StatusUnauthorized, wantDetail: "Sign in, or send an API token."},
		{name: "a status that does not exist", token: approverToken, path: actionProposalsRoute + "?status=maybe", wantStatus: http.StatusBadRequest, wantDetail: "status must be one of proposed, approved, rejected, expired, executing, succeeded, failed, undone."},
		{name: "a limit that is too large", token: approverToken, path: actionProposalsRoute + "?limit=101", wantStatus: http.StatusBadRequest, wantDetail: "limit must be 1 to 100."},
		{name: "a cursor that is not ours", token: approverToken, path: actionProposalsRoute + "?cursor=abc", wantStatus: http.StatusBadRequest, wantDetail: "cursor is not one this API returned."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var rec *httptest.ResponseRecorder
			if tt.token != "" {
				rec = s.doWithToken(t, http.MethodGet, tt.path, tt.token, nil)
			} else {
				rec = s.do(t, http.MethodGet, tt.path, "", "", nil)
			}

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, strings.SplitN(tt.path, "?", 2)[0]), rec.Body.String())
		})
	}
}

func TestApproveProposal(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	rec := s.do(t, http.MethodPost, actionProposalsRoute+"/"+propFirst+"/approve", apSession, apCSRF, nil)
	got := readActionProposal(t, rec)
	require.Equal(t, "approved", got.Status)
	require.Equal(t, "Ap", got.DecidedByName)
	require.NotEmpty(t, got.DecidedAt)

	require.Equal(t, []string{"action.approved user 01920000-0000-7000-8000-000000000070"}, actionProposalAudit(t, s.pool, propFirst))

	var jobs int
	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1 AND unique_key = $2", queue.KindExecuteAction, "action:"+propFirst).Scan(&jobs))
	require.Equal(t, 1, jobs, "the job that runs it is queued")

	// An API token approves too, and is audited as a token.
	readActionProposal(t, s.doWithToken(t, http.MethodPost, actionProposalsRoute+"/"+propSecond+"/approve", approverToken, nil))
	require.Equal(t, []string{"action.approved token 01920000-0000-7000-8000-000000000072"}, actionProposalAudit(t, s.pool, propSecond))
}

func TestRejectProposal(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	body := strings.NewReader(`{"note": "wrong board, mail me at ap@example.com"}`)
	got := readActionProposal(t, s.do(t, http.MethodPost, actionProposalsRoute+"/"+propFirst+"/reject", apSession, apCSRF, body))
	require.Equal(t, "rejected", got.Status)
	require.Equal(t, "Ap", got.DecidedByName)

	var details string
	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT details::text FROM audit_log WHERE action = 'action.rejected' AND target_id = $1", propFirst).Scan(&details))
	require.NotContains(t, details, "ap@example.com")
	require.Contains(t, details, "wrong board")

	// A rejection needs no body.
	readActionProposal(t, s.do(t, http.MethodPost, actionProposalsRoute+"/"+propSecond+"/reject", apSession, apCSRF, nil))

	var jobs int
	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindExecuteAction).Scan(&jobs))
	require.Zero(t, jobs, "a rejected proposal never runs")
}

func TestDecideProposalRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		change     func(policy action.StaticPolicy)
		id         string
		verb       string
		session    string
		csrf       string
		body       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			name: "an expired proposal cannot be approved", id: propExpired, verb: "approve", session: apSession, csrf: apCSRF,
			wantStatus: http.StatusConflict, wantCode: "expired", wantDetail: "This proposal expired before anyone decided it.",
		},
		{
			name: "an expired proposal cannot be rejected", id: propExpired, verb: "reject", session: apSession, csrf: apCSRF,
			wantStatus: http.StatusConflict, wantCode: "expired", wantDetail: "This proposal expired before anyone decided it.",
		},
		{
			name: "a proposal approved already", id: propApproved, verb: "approve", session: boSession, csrf: boCSRF,
			wantStatus: http.StatusConflict, wantCode: "decided", wantDetail: "This proposal is approved already; Ap decided it.",
		},
		{
			name: "a proposal rejected already", id: propRejected, verb: "approve", session: apSession, csrf: apCSRF,
			wantStatus: http.StatusConflict, wantCode: "decided", wantDetail: "This proposal is rejected already; Bo decided it.",
		},
		{
			name: "a proposal that ran", id: propSucceeded, verb: "reject", session: apSession, csrf: apCSRF,
			wantStatus: http.StatusConflict, wantCode: "decided", wantDetail: "This proposal is succeeded already; Ap decided it.",
		},
		{
			name: "a route that is read-only now", id: propFirst, verb: "approve", session: apSession, csrf: apCSRF,
			change: func(p action.StaticPolicy) {
				p["payments"] = action.RoutePolicy{Mode: action.ReadOnly, Actions: []string{"monitor-note"}}
			},
			wantStatus: http.StatusConflict, wantCode: "not_allowed", wantDetail: `The config does not allow this any more: route "payments" is read-only. Reject it instead.`,
		},
		{
			name: "a proposal that does not exist", id: propNotThere, verb: "approve", session: apSession, csrf: apCSRF,
			wantStatus: http.StatusNotFound, wantDetail: "No action proposal has this ID.",
		},
		{
			name: "a note that is too long", id: propFirst, verb: "reject", session: apSession, csrf: apCSRF, body: `{"note": "` + strings.Repeat("x", 501) + `"}`,
			wantStatus: http.StatusBadRequest, wantDetail: "note must be at most 500 characters.",
		},
		{
			name: "a viewer", id: propFirst, verb: "approve", session: viSession, csrf: viCSRF,
			wantStatus: http.StatusForbidden, wantDetail: "This needs the approver role.",
		},
		{
			name: "no CSRF token", id: propFirst, verb: "approve", session: apSession,
			wantStatus: http.StatusForbidden, wantDetail: "Send the session's CSRF token in the X-CSRF-Token header.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, policy := setupActionProposals(t)

			if tt.change != nil {
				tt.change(policy)
			}

			path := actionProposalsRoute + "/" + tt.id + "/" + tt.verb

			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}

			rec := s.do(t, http.MethodPost, path, tt.session, tt.csrf, body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			var got struct {
				Detail string `json:"detail"`
				Code   string `json:"code"`
			}

			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.wantDetail, got.Detail)
			require.Equal(t, tt.wantCode, got.Code)
			require.Empty(t, actionProposalAudit(t, s.pool, tt.id), "a refused decision writes nothing")
		})
	}
}

// TestTwoApproversAtOnceThroughTheAPI sends an approval and a rejection of
// the same proposal at the same moment: one gets 200 and the other 409.
func TestTwoApproversAtOnceThroughTheAPI(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	type result struct {
		code int
		body string
	}

	results := make(chan result, 2)

	for _, call := range []struct{ verb, session, csrf string }{{"approve", apSession, apCSRF}, {"reject", boSession, boCSRF}} {
		go func() {
			rec := s.do(t, http.MethodPost, actionProposalsRoute+"/"+propFirst+"/"+call.verb, call.session, call.csrf, nil)
			results <- result{rec.Code, rec.Body.String()}
		}()
	}

	first, second := <-results, <-results

	codes := []int{first.code, second.code}
	require.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, codes, "%s / %s", first.body, second.body)

	require.Len(t, actionProposalAudit(t, s.pool, propFirst), 1, "one decision is recorded")
}

func TestInvestigationHasItsProposals(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+propInvestgn, viSession, viCSRF, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var inv struct {
		Proposals []actionProposalBody `json:"proposals"`
	}

	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &inv))
	require.Len(t, inv.Proposals, 6)
	require.Equal(t, "monitor-note", inv.Proposals[0].Action)
}

func TestApproveWithoutTheService(t *testing.T) {
	t.Parallel()

	s := setupWith(t, nil, "fixture/proposals.sql")

	rec := s.do(t, http.MethodPost, actionProposalsRoute+"/"+propFirst+"/approve", apSession, apCSRF, nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// actionProposalAudit returns "<action> <actor kind> <actor id>" for each audit
// entry of the proposal with id, oldest first.
func actionProposalAudit(t *testing.T, pool *pgxpool.Pool, id string) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		"SELECT action || ' ' || actor_kind || ' ' || coalesce(actor_id, '') FROM audit_log WHERE target_kind = 'action_proposal' AND target_id = $1 ORDER BY id", id)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return out
}

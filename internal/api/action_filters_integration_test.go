//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The proposals of fixture/proposals_actions.sql, newest first.
const (
	propTicketNewest = "0192d3a4-0000-7000-8000-0000000000a1"
	propTicketFailed = "0192d3a4-0000-7000-8000-0000000000a2"
	propTicketOldest = "0192d3a4-0000-7000-8000-0000000000a3"
	propReactFailed  = "0192d3a4-0000-7000-8000-0000000000a4"
)

// setupActionFilters is setupActionProposals with the proposals of
// fixture/proposals_actions.sql, and three actions in the config.
func setupActionFilters(t *testing.T) server {
	t.Helper()

	s, _ := setupActionProposals(t)
	storetest.LoadFixture(t, s.pool, "fixture/proposals_actions.sql")

	s.cfg.Actions.Definitions = map[string]config.Section{"ticket-comment": {}, "monitor-note": {}, "slack-react": {}}

	return s
}

func TestListProposalsByAction(t *testing.T) {
	t.Parallel()

	s := setupActionFilters(t)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{name: "one action with the default status", query: "?action=monitor-note", want: []string{propSecond, propFirst, propExpired}},
		{name: "an empty action lists every action", query: "?action=", want: []string{propSecond, propFirst, propExpired}},
		{name: "one action and one status", query: "?action=ticket-comment&status=succeeded", want: []string{propTicketNewest, propTicketOldest}},
		{name: "one action and several statuses", query: "?action=ticket-comment&status=failed&status=succeeded", want: []string{propTicketNewest, propTicketFailed, propTicketOldest}},
		{name: "the other action is left out", query: "?action=monitor-note&status=succeeded&status=rejected&status=failed", want: []string{propSucceeded, propRejected}},
		{name: "an action with one proposal", query: "?action=slack-react&status=failed", want: []string{propReactFailed}},
		{name: "an action that has no proposal of the status", query: "?action=slack-react", want: []string{}},
		{name: "no action lists the actions together", query: "?status=failed", want: []string{propTicketFailed, propReactFailed}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+tt.query, approverToken, nil))
			require.Equal(t, tt.want, actionIDs(got.Proposals))
			require.Empty(t, got.NextCursor)
		})
	}
}

func TestListProposalsByActionPages(t *testing.T) {
	t.Parallel()

	s := setupActionFilters(t)

	const query = "?action=ticket-comment&status=succeeded&status=failed&limit=2"

	first := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+query, approverToken, nil))
	require.Equal(t, []string{propTicketNewest, propTicketFailed}, actionIDs(first.Proposals))
	require.NotEmpty(t, first.NextCursor)

	second := readActionProposals(t, s.doWithToken(t, http.MethodGet, actionProposalsRoute+query+"&cursor="+first.NextCursor, approverToken, nil))
	require.Equal(t, []string{propTicketOldest}, actionIDs(second.Proposals))
	require.Empty(t, second.NextCursor)
}

func TestListProposalsByActionRefusals(t *testing.T) {
	t.Parallel()

	s := setupActionFilters(t)
	noActions, _ := setupActionProposals(t)

	tests := []struct {
		name       string
		server     server
		token      string
		path       string
		wantStatus int
		wantDetail string
	}{
		{name: "an action the config does not define", server: s, token: approverToken, path: actionProposalsRoute + "?action=deploy", wantStatus: http.StatusBadRequest, wantDetail: "action must be one of monitor-note, slack-react, ticket-comment."},
		{name: "a name is not a prefix match", server: s, token: approverToken, path: actionProposalsRoute + "?action=monitor", wantStatus: http.StatusBadRequest, wantDetail: "action must be one of monitor-note, slack-react, ticket-comment."},
		{name: "an unknown action with a status and a cursor", server: s, token: approverToken, path: actionProposalsRoute + "?action=deploy&status=failed&cursor=abc", wantStatus: http.StatusBadRequest, wantDetail: "action must be one of monitor-note, slack-react, ticket-comment."},
		{name: "a config without actions", server: noActions, token: approverToken, path: actionProposalsRoute + "?action=monitor-note", wantStatus: http.StatusBadRequest, wantDetail: "No actions are configured, so no action can be filtered."},
		{name: "a viewer filtering", server: s, token: "skt_viewer", path: actionProposalsRoute + "?action=monitor-note", wantStatus: http.StatusForbidden, wantDetail: "This needs the approver role."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := tt.server.doWithToken(t, http.MethodGet, tt.path, tt.token, nil)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.JSONEq(t, problem(tt.wantStatus, tt.wantDetail, strings.SplitN(tt.path, "?", 2)[0]), rec.Body.String())
		})
	}
}

func TestListProposalActions(t *testing.T) {
	t.Parallel()

	withActions := setupActionFilters(t)
	withoutActions, _ := setupActionProposals(t)

	names := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var body struct {
			Actions []string `json:"actions"`
		}

		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.NotNil(t, body.Actions)

		return strings.Join(body.Actions, ",")
	}

	require.Equal(t, "monitor-note,slack-react,ticket-comment", names(t, withActions.doWithToken(t, http.MethodGet, actionProposalsRoute+"/actions", approverToken, nil)), "sorted")
	require.Empty(t, names(t, withoutActions.doWithToken(t, http.MethodGet, actionProposalsRoute+"/actions", approverToken, nil)))

	signedIn := withActions.do(t, http.MethodGet, actionProposalsRoute+"/actions", apSession, "", nil)
	require.Equal(t, "monitor-note,slack-react,ticket-comment", names(t, signedIn), "a person who is signed in may ask too")
}

func TestListProposalActionsRefusals(t *testing.T) {
	t.Parallel()

	s := setupActionFilters(t)
	path := actionProposalsRoute + "/actions"

	viewer := s.doWithToken(t, http.MethodGet, path, "skt_viewer", nil)
	require.Equal(t, http.StatusForbidden, viewer.Code, viewer.Body.String())
	require.JSONEq(t, problem(http.StatusForbidden, "This needs the approver role.", path), viewer.Body.String())

	anonymous := s.do(t, http.MethodGet, path, "", "", nil)
	require.Equal(t, http.StatusUnauthorized, anonymous.Code, anonymous.Body.String())
	require.JSONEq(t, problem(http.StatusUnauthorized, "Sign in, or send an API token.", path), anonymous.Body.String())
}

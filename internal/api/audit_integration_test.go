//go:build integration

package api_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// auditSeed is the audit log the tests of GET /audit read. Rows 3 and 4
// are made at the same moment, to test paging across equal times; row 7
// holds an email that the redaction rules mask.
const auditSeed = `
INSERT INTO audit_log (at, actor_kind, actor_id, action, target_kind, target_id, investigation_id, details) VALUES
  ('2026-10-20T10:00:00Z', 'user',     '01920000-0000-7000-8000-000000000040', 'route.created',    'route',    'a',  NULL, '{"new": {"priority": 1}}'),
  ('2026-10-20T10:01:00Z', 'user',     'U01SLACK',                             'listener.created', 'listener', 'x',  NULL, '{"new": {"channel": "#x"}}'),
  ('2026-10-20T10:02:00Z', 'policy',   NULL,                                   'action.executed',  'proposal', 'p1', '01920000-0000-7000-8000-0000000000a1', '{}'),
  ('2026-10-20T10:02:00Z', 'sidekick', NULL,                                   'action.proposed',  'proposal', 'p1', '01920000-0000-7000-8000-0000000000a1', '{}'),
  ('2026-10-20T10:03:00Z', 'token',    '01920000-0000-7000-8000-000000000042', 'job.retried',      'job',      '7',  NULL, '{"old": {"status": "dead"}, "new": {"status": "pending"}}'),
  ('2026-10-20T10:04:00Z', 'user',     '01920000-0000-7000-8000-000000000040', 'user.role_changed', 'user',    'u',  NULL, '{"old": "viewer", "new": "admin"}'),
  ('2026-10-20T10:05:00Z', 'user',     '01920000-0000-7000-8000-000000000040', 'route.updated',    'route',    'a',  NULL, '{"new": {"owner": "pager@example.com"}}');
`

// auditEntryView is an entry of GET /audit as the tests read it.
type auditEntryView struct {
	ID              int64  `json:"id"`
	At              string `json:"at"`
	ActorKind       string `json:"actorKind"`
	ActorID         string `json:"actorId"`
	ActorName       string `json:"actorName"`
	Action          string `json:"action"`
	TargetKind      string `json:"targetKind"`
	TargetID        string `json:"targetId"`
	InvestigationID string `json:"investigationId"`
	Old             any    `json:"old"`
	New             any    `json:"new"`
}

// auditPage is an answer of GET /audit.
type auditPage struct {
	Entries    []auditEntryView `json:"entries"`
	NextCursor string           `json:"nextCursor"`
}

// getAudit asks for the page that query selects, as c.
func (s server) getAudit(t *testing.T, c ruleCaller, query url.Values) (int, auditPage, *ruleProblem) {
	t.Helper()

	rec := s.sendRule(t, c, http.MethodGet, "/api/v1/audit?"+query.Encode(), nil)
	if rec.Code != http.StatusOK {
		p := ruleProblemOf(t, rec)

		return rec.Code, auditPage{}, &p
	}

	var page auditPage

	decodeRule(t, rec, &page)

	return rec.Code, page, nil
}

// ids returns the IDs of the entries.
func (p auditPage) ids() []int64 {
	out := make([]int64, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.ID
	}

	return out
}

func TestListAuditRoles(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	tests := []struct {
		who        ruleCaller
		wantStatus int
	}{
		{who: adminCaller, wantStatus: http.StatusOK},
		{who: adminTokenCaller, wantStatus: http.StatusOK},
		{who: responderCaller, wantStatus: http.StatusForbidden},
		{who: responderTokenCaller, wantStatus: http.StatusForbidden},
		{who: viewerCaller, wantStatus: http.StatusForbidden},
		{who: viewerTokenCaller, wantStatus: http.StatusForbidden},
		{who: ruleCaller{name: "nobody"}, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.who.name, func(t *testing.T) {
			t.Parallel()

			status, _, _ := s.getAudit(t, tt.who, url.Values{})
			require.Equal(t, tt.wantStatus, status)
		})
	}
}

func TestListAuditFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		query   url.Values
		wantIDs []int64
	}{
		{name: "no filter, newest first", query: url.Values{}, wantIDs: []int64{7, 6, 5, 4, 3, 2, 1}},
		{name: "from is inclusive", query: url.Values{"from": {"2026-10-20T10:03:00Z"}}, wantIDs: []int64{7, 6, 5}},
		{name: "to is exclusive", query: url.Values{"to": {"2026-10-20T10:02:00Z"}}, wantIDs: []int64{2, 1}},
		{name: "from and to", query: url.Values{"from": {"2026-10-20T10:01:00Z"}, "to": {"2026-10-20T10:03:00Z"}}, wantIDs: []int64{4, 3, 2}},
		{name: "a user", query: url.Values{"actor": {adminUserID}}, wantIDs: []int64{7, 6, 1}},
		{name: "a token", query: url.Values{"actor": {adminTokenID}}, wantIDs: []int64{5}},
		{name: "a Slack user", query: url.Values{"actor": {"U01SLACK"}}, wantIDs: []int64{2}},
		{name: "policy", query: url.Values{"actor": {"policy"}}, wantIDs: []int64{3}},
		{name: "sidekick", query: url.Values{"actor": {"sidekick"}}, wantIDs: []int64{4}},
		{name: "an actor nobody is", query: url.Values{"actor": {"nobody"}}, wantIDs: []int64{}},
		{name: "an action", query: url.Values{"action": {"route.updated"}}, wantIDs: []int64{7}},
		{name: "an investigation", query: url.Values{"investigation": {"01920000-0000-7000-8000-0000000000a1"}}, wantIDs: []int64{4, 3}},
		{name: "an actor and a time", query: url.Values{"actor": {adminUserID}, "to": {"2026-10-20T10:05:00Z"}}, wantIDs: []int64{6, 1}},
		{name: "an actor and an action", query: url.Values{"actor": {adminUserID}, "action": {"route.created"}}, wantIDs: []int64{1}},
	}

	s := setup(t)
	_, err := s.pool.Exec(t.Context(), auditSeed)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, page, _ := s.getAudit(t, adminCaller, tt.query)
			require.Equal(t, http.StatusOK, status)
			require.Equal(t, tt.wantIDs, page.ids())
			require.Empty(t, page.NextCursor)
		})
	}
}

func TestListAuditPages(t *testing.T) {
	t.Parallel()

	s := setup(t)
	_, err := s.pool.Exec(t.Context(), auditSeed)
	require.NoError(t, err)

	var (
		got    []int64
		cursor string
		pages  int
	)

	for {
		query := url.Values{"limit": {"3"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}

		status, page, _ := s.getAudit(t, adminCaller, query)
		require.Equal(t, http.StatusOK, status)

		got = append(got, page.ids()...)
		pages++
		cursor = page.NextCursor

		if cursor == "" {
			break
		}

		require.Len(t, page.Entries, 3, "only the last page is short")
		require.Less(t, pages, 10, "paging never ends")
	}

	require.Equal(t, []int64{7, 6, 5, 4, 3, 2, 1}, got, "rows made at the same moment are neither skipped nor repeated")
	require.Equal(t, 3, pages)
}

func TestListAuditPagesWithAFilter(t *testing.T) {
	t.Parallel()

	s := setup(t)
	_, err := s.pool.Exec(t.Context(), auditSeed)
	require.NoError(t, err)

	_, first, _ := s.getAudit(t, adminCaller, url.Values{"actor": {adminUserID}, "limit": {"2"}})
	require.Equal(t, []int64{7, 6}, first.ids())
	require.NotEmpty(t, first.NextCursor)

	_, second, _ := s.getAudit(t, adminCaller, url.Values{"actor": {adminUserID}, "limit": {"2"}, "cursor": {first.NextCursor}})
	require.Equal(t, []int64{1}, second.ids())
	require.Empty(t, second.NextCursor)
}

func TestListAuditEntries(t *testing.T) {
	t.Parallel()

	s := setup(t)
	_, err := s.pool.Exec(t.Context(), auditSeed)
	require.NoError(t, err)

	_, page, _ := s.getAudit(t, adminCaller, url.Values{})

	byID := map[int64]auditEntryView{}
	for _, e := range page.Entries {
		byID[e.ID] = e
	}

	t.Run("a user has a name", func(t *testing.T) {
		t.Parallel()

		e := byID[1]
		require.Equal(t, "user", e.ActorKind)
		require.Equal(t, adminUserID, e.ActorID)
		require.Equal(t, "Ad", e.ActorName)
		require.NotEmpty(t, e.At)
		require.Equal(t, "route", e.TargetKind)
		require.Equal(t, "a", e.TargetID)
		require.InDelta(t, 1, e.New.(map[string]any)["priority"], 0)
		require.Nil(t, e.Old)
	})

	t.Run("a token has a name", func(t *testing.T) {
		t.Parallel()

		e := byID[5]
		require.Equal(t, "token", e.ActorKind)
		require.Equal(t, "ops bot", e.ActorName)
		require.Equal(t, map[string]any{"status": "dead"}, e.Old)
		require.Equal(t, map[string]any{"status": "pending"}, e.New)
	})

	t.Run("a Slack user has no name", func(t *testing.T) {
		t.Parallel()

		e := byID[2]
		require.Equal(t, "U01SLACK", e.ActorID)
		require.Empty(t, e.ActorName)
	})

	t.Run("policy and sidekick have no actor ID", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "policy", byID[3].ActorKind)
		require.Empty(t, byID[3].ActorID)
		require.Equal(t, "01920000-0000-7000-8000-0000000000a1", byID[3].InvestigationID)
		require.Equal(t, "sidekick", byID[4].ActorKind)
	})

	t.Run("an old value that is not an object", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "viewer", byID[6].Old)
	})

	t.Run("a value that a redaction rule matches is masked", func(t *testing.T) {
		t.Parallel()

		require.NotContains(t, byID[7].New.(map[string]any)["owner"], "pager@example.com")
	})
}

func TestListAuditRefusesBadParameters(t *testing.T) {
	t.Parallel()

	s := setup(t)

	tests := []struct {
		name  string
		query url.Values
	}{
		{name: "a limit of zero", query: url.Values{"limit": {"0"}}},
		{name: "a limit above the most", query: url.Values{"limit": {"201"}}},
		{name: "a limit that is not a number", query: url.Values{"limit": {"many"}}},
		{name: "a cursor that is not one", query: url.Values{"cursor": {"not-a-cursor"}}},
		{name: "a time that is not a time", query: url.Values{"from": {"yesterday"}}},
		{name: "an investigation that is not a UUID", query: url.Values{"investigation": {"inv-1"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, _, p := s.getAudit(t, adminCaller, tt.query)
			require.Equal(t, http.StatusBadRequest, status)
			require.NotNil(t, p)
		})
	}
}

func TestChangesShowInTheAuditLog(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	rec := s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/routes", map[string]any{
		"name": "search", "match": map[string]any{"service": "search-*"}, "outputs": []string{"slack-thread"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = s.sendRule(t, responderTokenCaller, http.MethodPost, "/api/v1/listeners", map[string]any{"name": "billing", "channel": "#billing"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	_, page, _ := s.getAudit(t, adminCaller, url.Values{})
	require.Len(t, page.Entries, 2)

	require.Equal(t, "listener.created", page.Entries[0].Action)
	require.Equal(t, "token", page.Entries[0].ActorKind)
	require.Equal(t, "responder bot", page.Entries[0].ActorName)
	require.Equal(t, "billing", page.Entries[0].TargetID)

	require.Equal(t, "route.created", page.Entries[1].Action)
	require.Equal(t, "user", page.Entries[1].ActorKind)
	require.Equal(t, "Ad", page.Entries[1].ActorName)

	_, byActor, _ := s.getAudit(t, adminCaller, url.Values{"actor": {responderTokenID}})
	require.Equal(t, []int64{page.Entries[0].ID}, byActor.ids())
}

//go:build integration

package api_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// ruleItem is a listener or a route of the lists, with the fields the
// tests look at.
type ruleItem struct {
	Name         string         `json:"name"`
	Layer        string         `json:"layer"`
	Status       string         `json:"status"`
	StatusReason string         `json:"statusReason"`
	ReadOnly     bool           `json:"readOnly"`
	Enabled      bool           `json:"enabled"`
	Version      int            `json:"version"`
	UpdatedBy    string         `json:"updatedBy"`
	Position     int            `json:"position"`
	Channel      string         `json:"channel"`
	Priority     int            `json:"priority"`
	Mode         string         `json:"mode"`
	Match        map[string]any `json:"match"`
	Budgets      map[string]any `json:"budgets"`
	Sources      []string       `json:"sources"`
}

// ruleRef names a rule by layer and name, with its status.
type ruleRef struct {
	Name, Layer, Status string
}

// refs returns the layer, name and status of each item.
func refs(items []ruleItem) []ruleRef {
	out := make([]ruleRef, len(items))
	for i, it := range items {
		out[i] = ruleRef{Name: it.Name, Layer: it.Layer, Status: it.Status}
	}

	return out
}

// listListeners returns the listeners as c sees them.
func (s server) listListeners(t *testing.T, c ruleCaller) []ruleItem {
	t.Helper()

	rec := s.sendRule(t, c, http.MethodGet, "/api/v1/listeners", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		Listeners []ruleItem `json:"listeners"`
	}

	decodeRule(t, rec, &got)

	return got.Listeners
}

// listRoutes returns the routes as c sees them.
func (s server) listRoutes(t *testing.T, c ruleCaller) []ruleItem {
	t.Helper()

	rec := s.sendRule(t, c, http.MethodGet, "/api/v1/routes", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got struct {
		Routes []ruleItem `json:"routes"`
	}

	decodeRule(t, rec, &got)

	return got.Routes
}

// find returns the item named name of layer.
func find(t *testing.T, items []ruleItem, layer, name string) ruleItem {
	t.Helper()

	i := slices.IndexFunc(items, func(it ruleItem) bool { return it.Name == name && it.Layer == layer })
	require.GreaterOrEqual(t, i, 0, "no %s item named %s in %v", layer, name, refs(items))

	return items[i]
}

// wantActor is the kind and ID that the audit log records for c.
func wantActor(c ruleCaller) (kind, id string) {
	switch c.name {
	case responderCaller.name:
		return audit.ActorUser, responderUserID
	case responderTokenCaller.name:
		return audit.ActorToken, responderTokenID
	case adminCaller.name:
		return audit.ActorUser, adminUserID
	case adminTokenCaller.name:
		return audit.ActorToken, adminTokenID
	default:
		return "", ""
	}
}

func TestListListeners(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	tests := []struct {
		who        ruleCaller
		wantStatus int
	}{
		{who: viewerCaller, wantStatus: http.StatusOK},
		{who: viewerTokenCaller, wantStatus: http.StatusOK},
		{who: adminCaller, wantStatus: http.StatusOK},
		{who: ruleCaller{name: "nobody"}, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.who.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, tt.who, http.MethodGet, "/api/v1/listeners", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}

	t.Run("the file listener is read-only and the shadowed runtime one is flagged", func(t *testing.T) {
		t.Parallel()

		items := s.listListeners(t, viewerCaller)

		require.Equal(t, []ruleRef{
			{"alerts", "file", "active"},
			{"ops", "runtime", "active"},
			{"alerts", "runtime", "shadowed"},
		}, refs(items))

		file := find(t, items, "file", "alerts")
		require.True(t, file.ReadOnly)
		require.Equal(t, "#alerts", file.Channel)
		require.Zero(t, file.Version)

		ops := find(t, items, "runtime", "ops")
		require.False(t, ops.ReadOnly)
		require.True(t, ops.Enabled)
		require.Equal(t, 2, ops.Version)
		require.Equal(t, "U01SEED", ops.UpdatedBy)
		require.Equal(t, "bot", ops.Match["sender"].(map[string]any)["kind"])

		shadowed := find(t, items, "runtime", "alerts")
		require.Contains(t, shadowed.StatusReason, "config file")
		require.Equal(t, "#old-alerts", shadowed.Channel)
	})
}

func TestCreateListener(t *testing.T) {
	t.Parallel()

	billing := map[string]any{
		"name": "billing", "channel": "#billing",
		"match": map[string]any{"sender": map[string]any{"kind": "bot", "names": []string{"Grafana"}}, "text": "(?i)firing"},
	}

	with := func(key string, value any) map[string]any {
		body := map[string]any{}
		for k, v := range billing {
			body[k] = v
		}

		body[key] = value

		return body
	}

	tests := []struct {
		name       string
		who        ruleCaller
		body       map[string]any
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{name: "a responder", who: responderCaller, body: billing, wantStatus: http.StatusCreated},
		{name: "a responder token", who: responderTokenCaller, body: billing, wantStatus: http.StatusCreated},
		{name: "an admin", who: adminCaller, body: billing, wantStatus: http.StatusCreated},
		{name: "a viewer", who: viewerCaller, body: billing, wantStatus: http.StatusForbidden},
		{name: "a viewer token", who: viewerTokenCaller, body: billing, wantStatus: http.StatusForbidden},
		{name: "nobody", who: ruleCaller{}, body: billing, wantStatus: http.StatusUnauthorized},
		{name: "the name of a file listener", who: responderCaller, body: with("name", "alerts"), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "the name of a runtime listener", who: responderCaller, body: with("name", "ops"), wantStatus: http.StatusConflict, wantCode: "exists"},
		{
			name: "a pattern that does not parse", who: responderCaller,
			body: with("match", map[string]any{"text": "(firing"}), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: []string{"match.text"},
		},
		{
			name: "a channel that is not a channel", who: responderCaller,
			body: with("channel", "billing"), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: []string{"channel"},
		},
		{
			name: "a name that is not a name", who: responderCaller,
			body: with("name", "Billing Team"), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: []string{"name"},
		},
		{
			name: "an extract pattern without a named group", who: responderCaller,
			body: with("extract", []string{"firing"}), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: []string{"extract[0]"},
		},
		{
			name: "a bot ID that is not one", who: responderCaller,
			body:       with("match", map[string]any{"sender": map[string]any{"bots": []string{"grafana"}}}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: []string{"match.sender.bots[0]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodPost, "/api/v1/listeners", tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			audits := s.ruleAudits(t)

			if tt.wantStatus != http.StatusCreated {
				require.Empty(t, audits, "a refused change writes no audit row")
				require.Len(t, s.listListeners(t, adminCaller), 3, "a refused change stores nothing")

				if tt.wantCode != "" {
					p := ruleProblemOf(t, rec)
					require.Equal(t, tt.wantCode, p.Code)

					messages := p.fieldMessages()
					for _, field := range tt.wantFields {
						require.Contains(t, messages, field)
					}
				}

				return
			}

			var created ruleItem
			decodeRule(t, rec, &created)
			require.Equal(t, "billing", created.Name)
			require.Equal(t, "runtime", created.Layer)
			require.Equal(t, "active", created.Status)
			require.Equal(t, 1, created.Version)
			require.Equal(t, "#billing", created.Channel)

			kind, id := wantActor(tt.who)
			require.Len(t, audits, 1)
			require.Equal(t, ruleAudit{
				ActorKind: kind, ActorID: id, Action: rules.ActionListenerCreated, Target: "listener:billing", Details: audits[0].Details,
			}, audits[0])
			require.Equal(t, "#billing", audits[0].Details["new"].(map[string]any)["channel"])
			require.Equal(t, id, find(t, s.listListeners(t, viewerCaller), "runtime", "billing").UpdatedBy)
		})
	}
}

func TestUpdateListener(t *testing.T) {
	t.Parallel()

	moved := func(version int) map[string]any {
		return map[string]any{
			"channel": "#ops-2", "enabled": true, "version": version,
			"match": map[string]any{"sender": map[string]any{"kind": "bot"}},
		}
	}

	tests := []struct {
		name       string
		who        ruleCaller
		path       string
		body       map[string]any
		wantStatus int
		wantCode   string
		wantFields []string
	}{
		{name: "a responder", who: responderCaller, path: "ops", body: moved(2), wantStatus: http.StatusOK},
		{name: "an admin token", who: adminTokenCaller, path: "ops", body: moved(2), wantStatus: http.StatusOK},
		{name: "a viewer", who: viewerCaller, path: "ops", body: moved(2), wantStatus: http.StatusForbidden},
		{name: "a version that is out of date", who: responderCaller, path: "ops", body: moved(1), wantStatus: http.StatusConflict, wantCode: "stale"},
		{name: "a file listener", who: responderCaller, path: "alerts", body: moved(1), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "a listener that does not exist", who: responderCaller, path: "nope", body: moved(1), wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{
			name: "a channel that is not a channel", who: responderCaller, path: "ops",
			body: map[string]any{"channel": "ops", "enabled": true, "version": 2}, wantStatus: http.StatusUnprocessableEntity, wantCode: "validation",
			wantFields: []string{"channel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodPut, "/api/v1/listeners/"+tt.path, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			ops := find(t, s.listListeners(t, viewerCaller), "runtime", "ops")

			if tt.wantStatus != http.StatusOK {
				require.Empty(t, s.ruleAudits(t))
				require.Equal(t, 2, ops.Version)
				require.Equal(t, "#ops", ops.Channel)

				if tt.wantCode != "" {
					p := ruleProblemOf(t, rec)
					require.Equal(t, tt.wantCode, p.Code)

					for _, field := range tt.wantFields {
						require.Contains(t, p.fieldMessages(), field)
					}
				}

				return
			}

			var saved ruleItem
			decodeRule(t, rec, &saved)
			require.Equal(t, 3, saved.Version)
			require.Equal(t, "#ops-2", saved.Channel)
			require.Equal(t, saved, ops)

			audits := s.ruleAudits(t)
			require.Len(t, audits, 1)
			require.Equal(t, rules.ActionListenerUpdated, audits[0].Action)
			require.Equal(t, "#ops", audits[0].Details["old"].(map[string]any)["channel"])
			require.Equal(t, "#ops-2", audits[0].Details["new"].(map[string]any)["channel"])
		})
	}
}

func TestUpdateListenerTurnsItOff(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	rec := s.sendRule(t, responderCaller, http.MethodPut, "/api/v1/listeners/ops", map[string]any{
		"channel": "#ops", "enabled": false, "version": 2, "match": map[string]any{"sender": map[string]any{"kind": "bot"}},
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var saved ruleItem
	decodeRule(t, rec, &saved)
	require.Equal(t, "disabled", saved.Status)
	require.False(t, saved.Enabled)
	require.Equal(t, 3, saved.Version)
}

func TestDeleteListener(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		who        ruleCaller
		path       string
		wantStatus int
		wantCode   string
		wantLeft   []ruleRef
	}{
		{
			name: "a runtime listener", who: responderCaller, path: "ops", wantStatus: http.StatusNoContent,
			wantLeft: []ruleRef{{"alerts", "file", "active"}, {"alerts", "runtime", "shadowed"}},
		},
		{
			name: "a runtime listener that a file listener shadows", who: responderTokenCaller, path: "alerts", wantStatus: http.StatusNoContent,
			wantLeft: []ruleRef{{"alerts", "file", "active"}, {"ops", "runtime", "active"}},
		},
		{
			name: "a viewer", who: viewerCaller, path: "ops", wantStatus: http.StatusForbidden,
			wantLeft: []ruleRef{{"alerts", "file", "active"}, {"ops", "runtime", "active"}, {"alerts", "runtime", "shadowed"}},
		},
		{
			name: "a listener that does not exist", who: responderCaller, path: "nope", wantStatus: http.StatusNotFound, wantCode: "not_found",
			wantLeft: []ruleRef{{"alerts", "file", "active"}, {"ops", "runtime", "active"}, {"alerts", "runtime", "shadowed"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodDelete, "/api/v1/listeners/"+tt.path, nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantLeft, refs(s.listListeners(t, adminCaller)))

			audits := s.ruleAudits(t)

			if tt.wantStatus == http.StatusNoContent {
				require.Len(t, audits, 1)
				require.Equal(t, rules.ActionListenerDeleted, audits[0].Action)
				require.Equal(t, "listener:"+tt.path, audits[0].Target)

				return
			}

			require.Empty(t, audits)

			if tt.wantCode != "" {
				require.Equal(t, tt.wantCode, ruleProblemOf(t, rec).Code)
			}
		})
	}
}

func TestDeleteListenerOfTheFile(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	rec := s.sendRule(t, responderCaller, http.MethodDelete, "/api/v1/listeners/alerts", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, "the first delete removes the shadowed runtime copy")

	rec = s.sendRule(t, responderCaller, http.MethodDelete, "/api/v1/listeners/alerts", nil)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	p := ruleProblemOf(t, rec)
	require.Equal(t, "file_rule", p.Code)
	require.Contains(t, p.Detail, "config file sidekick.yaml")
	require.Contains(t, p.Detail, "alerts")
}

func TestListRoutes(t *testing.T) {
	t.Parallel()

	s := setupRules(t)

	tests := []struct {
		who        ruleCaller
		wantStatus int
	}{
		{who: viewerCaller, wantStatus: http.StatusOK},
		{who: viewerTokenCaller, wantStatus: http.StatusOK},
		{who: ruleCaller{name: "nobody"}, wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.who.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, tt.who, http.MethodGet, "/api/v1/routes", nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
		})
	}

	t.Run("routes in effect come in the order they are tried, then the others with their status", func(t *testing.T) {
		t.Parallel()

		items := s.listRoutes(t, viewerCaller)

		require.Equal(t, []ruleRef{
			{"payments", "file", "active"},
			{"default", "file", "active"},
			{"broken", "runtime", "invalid"},
			{"checkout", "runtime", "disabled"},
			{"payments", "runtime", "shadowed"},
		}, refs(items))

		payments := find(t, items, "file", "payments")
		require.True(t, payments.ReadOnly)
		require.Equal(t, 1, payments.Position)
		require.Equal(t, 50, payments.Priority)
		require.Equal(t, "read-only", payments.Mode)
		require.Equal(t, "payments-*", payments.Match["service"])

		require.Equal(t, 2, find(t, items, "file", "default").Position)
		require.Nil(t, find(t, items, "file", "default").Match)

		broken := find(t, items, "runtime", "broken")
		require.Contains(t, broken.StatusReason, "gone")
		require.Zero(t, broken.Position)

		checkout := find(t, items, "runtime", "checkout")
		require.False(t, checkout.Enabled)
		require.Equal(t, 1, checkout.Version)
		require.Equal(t, 20, checkout.Priority)

		require.Contains(t, find(t, items, "runtime", "payments").StatusReason, "config file")
	})
}

func TestCreateRoute(t *testing.T) {
	t.Parallel()

	search := map[string]any{
		"name": "search", "priority": 10, "match": map[string]any{"service": "search-*"}, "outputs": []string{"slack-thread"},
		"budgets": map[string]any{"maxToolCalls": 20, "maxWallTime": "5m"}, "grouping": map[string]any{"window": "20m"},
	}

	with := func(key string, value any) map[string]any {
		body := map[string]any{}
		for k, v := range search {
			body[k] = v
		}

		if value == nil {
			delete(body, key)
		} else {
			body[key] = value
		}

		return body
	}

	tests := []struct {
		name       string
		who        ruleCaller
		body       map[string]any
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{name: "an admin", who: adminCaller, body: search, wantStatus: http.StatusCreated},
		{name: "an admin token", who: adminTokenCaller, body: search, wantStatus: http.StatusCreated},
		{name: "a responder", who: responderCaller, body: search, wantStatus: http.StatusForbidden},
		{name: "a responder token", who: responderTokenCaller, body: search, wantStatus: http.StatusForbidden},
		{name: "a viewer", who: viewerCaller, body: search, wantStatus: http.StatusForbidden},
		{name: "nobody", who: ruleCaller{}, body: search, wantStatus: http.StatusUnauthorized},
		{name: "the name of a file route", who: adminCaller, body: with("name", "payments"), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "the default route", who: adminCaller, body: with("name", "default"), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "the name of a runtime route", who: adminCaller, body: with("name", "checkout"), wantStatus: http.StatusConflict, wantCode: "exists"},
		{name: "a route without a match", who: adminCaller, body: with("match", nil), wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "match"},
		{
			name: "a budget above the ceiling", who: adminCaller, body: with("budgets", map[string]any{"maxToolCalls": ruleCeilingToolCalls + 1}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "budgets.maxToolCalls",
		},
		{
			name: "a wall time above the ceiling", who: adminCaller, body: with("budgets", map[string]any{"maxWallTime": "1h"}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "budgets.maxWallTime",
		},
		{
			name: "a source the file does not define", who: adminCaller, body: with("sources", []string{"gone"}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "sources[0]",
		},
		{
			name: "an output the file does not define", who: adminCaller, body: with("outputs", []string{"slack-thread", "gone"}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "outputs[1]",
		},
		{
			name: "a mode that does not exist yet", who: adminCaller, body: with("mode", "approved"),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "mode",
		},
		{
			name: "an intake that does not exist", who: adminCaller, body: with("match", map[string]any{"intake": []string{"gone"}}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "match.intake[0]",
		},
		{
			name: "a pattern that does not parse", who: adminCaller, body: with("match", map[string]any{"text": "(boom"}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "match.text",
		},
		{
			name: "a severity that does not exist", who: adminCaller, body: with("match", map[string]any{"severity": []string{"loud"}}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "match.severity[0]",
		},
		{
			name: "a duration that does not parse", who: adminCaller, body: with("grouping", map[string]any{"window": "20 minutes"}),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "grouping.window",
		},
		{
			name: "a name that is not a name", who: adminCaller, body: with("name", "Search Team"),
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodPost, "/api/v1/routes", tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			audits := s.ruleAudits(t)

			if tt.wantStatus != http.StatusCreated {
				require.Empty(t, audits, "a refused change writes no audit row")
				require.Len(t, s.listRoutes(t, adminCaller), 5, "a refused change stores nothing")

				if tt.wantCode != "" {
					p := ruleProblemOf(t, rec)
					require.Equal(t, tt.wantCode, p.Code)

					if tt.wantField != "" {
						require.Contains(t, p.fieldMessages(), tt.wantField)
					}
				}

				return
			}

			var created ruleItem
			decodeRule(t, rec, &created)
			require.Equal(t, "search", created.Name)
			require.Equal(t, "active", created.Status)
			require.Equal(t, 1, created.Version)
			require.Equal(t, 1, created.Position, "priority 10 is tried first")
			require.Equal(t, "read-only", created.Mode)
			require.EqualValues(t, 20, created.Budgets["maxToolCalls"])
			require.Equal(t, "5m0s", created.Budgets["maxWallTime"])

			kind, id := wantActor(tt.who)
			require.Len(t, audits, 1)
			require.Equal(t, kind, audits[0].ActorKind)
			require.Equal(t, id, audits[0].ActorID)
			require.Equal(t, rules.ActionRouteCreated, audits[0].Action)
			require.Equal(t, "route:search", audits[0].Target)
			require.InDelta(t, 10, audits[0].Details["new"].(map[string]any)["priority"], 0)
		})
	}
}

func TestUpdateRoute(t *testing.T) {
	t.Parallel()

	checkout := func(version int, enabled bool) map[string]any {
		return map[string]any{
			"priority": 5, "enabled": enabled, "version": version, "match": map[string]any{"service": "checkout-*"},
			"outputs": []string{"slack-thread"}, "budgets": map[string]any{"maxToolCalls": 30},
		}
	}

	tests := []struct {
		name       string
		who        ruleCaller
		path       string
		body       map[string]any
		wantStatus int
		wantCode   string
		wantField  string
	}{
		{name: "an admin turns it on", who: adminCaller, path: "checkout", body: checkout(1, true), wantStatus: http.StatusOK},
		{name: "an admin token", who: adminTokenCaller, path: "checkout", body: checkout(1, false), wantStatus: http.StatusOK},
		{name: "a responder", who: responderCaller, path: "checkout", body: checkout(1, true), wantStatus: http.StatusForbidden},
		{name: "a version that is out of date", who: adminCaller, path: "checkout", body: checkout(0, true), wantStatus: http.StatusConflict, wantCode: "stale"},
		{name: "a file route", who: adminCaller, path: "payments", body: checkout(1, true), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "the default route", who: adminCaller, path: "default", body: checkout(1, true), wantStatus: http.StatusConflict, wantCode: "file_rule"},
		{name: "a route that does not exist", who: adminCaller, path: "nope", body: checkout(1, true), wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{
			name: "a budget above the ceiling", who: adminCaller, path: "checkout",
			body:       map[string]any{"enabled": true, "version": 1, "match": map[string]any{"service": "x-*"}, "budgets": map[string]any{"maxToolCalls": 100}},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantField: "budgets.maxToolCalls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodPut, "/api/v1/routes/"+tt.path, tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			stored := find(t, s.listRoutes(t, viewerCaller), "runtime", "checkout")

			if tt.wantStatus != http.StatusOK {
				require.Empty(t, s.ruleAudits(t))
				require.Equal(t, 1, stored.Version)
				require.Equal(t, 20, stored.Priority)

				if tt.wantCode != "" {
					p := ruleProblemOf(t, rec)
					require.Equal(t, tt.wantCode, p.Code)

					if tt.wantField != "" {
						require.Contains(t, p.fieldMessages(), tt.wantField)
					}
				}

				return
			}

			var saved ruleItem
			decodeRule(t, rec, &saved)
			require.Equal(t, 2, saved.Version)
			require.Equal(t, 5, saved.Priority)
			require.Equal(t, saved, stored)
			require.Equal(t, tt.body["enabled"], saved.Enabled)

			if saved.Enabled {
				require.Equal(t, "active", saved.Status)
				require.Equal(t, 1, saved.Position, "priority 5 is tried before the payments route")
			} else {
				require.Equal(t, "disabled", saved.Status)
			}

			audits := s.ruleAudits(t)
			require.Len(t, audits, 1)
			require.Equal(t, rules.ActionRouteUpdated, audits[0].Action)
			require.InDelta(t, 20, audits[0].Details["old"].(map[string]any)["priority"], 0)
			require.InDelta(t, 5, audits[0].Details["new"].(map[string]any)["priority"], 0)
		})
	}
}

func TestDeleteRoute(t *testing.T) {
	t.Parallel()

	all := []ruleRef{
		{"payments", "file", "active"}, {"default", "file", "active"},
		{"broken", "runtime", "invalid"}, {"checkout", "runtime", "disabled"}, {"payments", "runtime", "shadowed"},
	}

	without := func(layer, name string) []ruleRef {
		return slices.DeleteFunc(slices.Clone(all), func(r ruleRef) bool { return r.Layer == layer && r.Name == name })
	}

	tests := []struct {
		name       string
		who        ruleCaller
		path       string
		wantStatus int
		wantCode   string
		wantLeft   []ruleRef
	}{
		{name: "a runtime route", who: adminCaller, path: "checkout", wantStatus: http.StatusNoContent, wantLeft: without("runtime", "checkout")},
		{name: "an invalid route", who: adminTokenCaller, path: "broken", wantStatus: http.StatusNoContent, wantLeft: without("runtime", "broken")},
		{name: "a runtime route that a file route shadows", who: adminCaller, path: "payments", wantStatus: http.StatusNoContent, wantLeft: without("runtime", "payments")},
		{name: "a responder", who: responderCaller, path: "checkout", wantStatus: http.StatusForbidden, wantLeft: all},
		{name: "the default route", who: adminCaller, path: "default", wantStatus: http.StatusConflict, wantCode: "file_rule", wantLeft: all},
		{name: "a route that does not exist", who: adminCaller, path: "nope", wantStatus: http.StatusNotFound, wantCode: "not_found", wantLeft: all},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupRules(t)

			rec := s.sendRule(t, tt.who, http.MethodDelete, "/api/v1/routes/"+tt.path, nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tt.wantLeft, refs(s.listRoutes(t, adminCaller)))

			audits := s.ruleAudits(t)

			if tt.wantStatus == http.StatusNoContent {
				require.Len(t, audits, 1)
				require.Equal(t, rules.ActionRouteDeleted, audits[0].Action)
				require.Equal(t, "route:"+tt.path, audits[0].Target)

				return
			}

			require.Empty(t, audits)

			if tt.wantCode != "" {
				require.Equal(t, tt.wantCode, ruleProblemOf(t, rec).Code)
			}
		})
	}
}

func TestRuleChangesAreUsedByAnotherReplica(t *testing.T) {
	t.Parallel()

	// notifyDeadline is how soon another replica must use a rule saved
	// through the API. The reload interval is an hour, so only the
	// notification can make it in time.
	const notifyDeadline = 2 * time.Second

	s := setupRules(t)
	other := s.replica(t, nil)

	cache := rules.NewCache(t.Context(), rules.Options{File: s.cfg, Pool: s.pool, Interval: time.Hour})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- cache.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})

	require.Eventually(t, func() bool { return !cache.Current().LoadedAt.IsZero() }, notifyDeadline, 10*time.Millisecond)

	routeNames := func() []string {
		var names []string
		for _, r := range cache.Current().Routes {
			names = append(names, r.Config.Name)
		}

		return names
	}

	require.NotContains(t, routeNames(), "search")

	rec := s.sendRule(t, adminCaller, http.MethodPost, "/api/v1/routes", map[string]any{
		"name": "search", "priority": 10, "match": map[string]any{"service": "search-*"}, "outputs": []string{"slack-thread"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	saved := time.Now()

	require.Eventually(t, func() bool { return slices.Contains(routeNames(), "search") }, notifyDeadline, 10*time.Millisecond)
	t.Logf("the other replica used the route %s after it was saved", time.Since(saved))
	require.Equal(t, "search", cache.Current().Routes[0].Config.Name, "priority 10 is tried first")
	require.Equal(t, rules.LayerRuntime, cache.Current().Routes[0].Layer)

	require.Contains(t, refs(other.listRoutes(t, viewerCaller)), ruleRef{"search", "runtime", "active"})

	rec = s.sendRule(t, responderCaller, http.MethodPost, "/api/v1/listeners", map[string]any{"name": "billing", "channel": "#billing"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	require.Eventually(t, func() bool {
		_, ok := cache.Current().Listener("billing")

		return ok
	}, notifyDeadline, 10*time.Millisecond)

	rec = s.sendRule(t, adminCaller, http.MethodDelete, "/api/v1/routes/search", nil)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	require.Eventually(t, func() bool { return !slices.Contains(routeNames(), "search") }, notifyDeadline, 10*time.Millisecond)
}

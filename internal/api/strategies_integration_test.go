//go:build integration

package api_test

import (
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
	"github.com/nourtalaat/sidekick/strategies"
)

const strategiesPath = "/api/v1/strategies"

// strategyItem is a strategy of the list, with the fields the tests look at.
type strategyItem struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Layer          string   `json:"layer"`
	Status         string   `json:"status"`
	StatusReason   string   `json:"statusReason"`
	ReadOnly       bool     `json:"readOnly"`
	Version        string   `json:"version"`
	RuntimeVersion int      `json:"runtimeVersion"`
	Overrides      []string `json:"overrides"`
	UpdatedBy      string   `json:"updatedBy"`
	Body           string   `json:"body"`
}

// versionItem is a saved version, with the fields the tests look at.
type versionItem struct {
	Name       string `json:"name"`
	Version    int    `json:"version"`
	Reason     string `json:"reason"`
	AuthorName string `json:"authorName"`
	Current    bool   `json:"current"`
	Body       string `json:"body"`
}

// setupStrategies is setup with the built-in strategies, a strategy file
// named file-latency, the runtime strategies of fixture/strategies_api.sql
// and the responder of fixture/rules_api.sql.
func setupStrategies(t *testing.T) server {
	t.Helper()

	base, err := strategy.Load(strategies.FS, filepath.Join("fixture", "strategies"), nil)
	require.NoError(t, err)

	return setupWith(t, func(o *api.Options) {
		o.Strategies = strategystore.NewStore(o.Pool)
		o.StrategyBase = base
	}, "fixture/rules_api.sql", "fixture/strategies_api.sql")
}

func TestListStrategies(t *testing.T) {
	t.Parallel()

	s := setupStrategies(t)

	for _, c := range []ruleCaller{viewerCaller, viewerTokenCaller} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, c, http.MethodGet, strategiesPath, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var got struct {
				Strategies []strategyItem `json:"strategies"`
			}

			decodeRule(t, rec, &got)

			type ref struct{ Name, Layer, Status string }

			var refs []ref
			for _, it := range got.Strategies {
				refs = append(refs, ref{it.Name, it.Layer, it.Status})
			}

			require.Equal(t, []ref{
				{"checkout-latency", "runtime", "active"},
				{"error-rate-alert", "built-in", "active"},
				{"file-latency", "file", "active"},
				{"generic-alert", "built-in", "active"},
				{"latency-alert", "built-in", "active"},
				{"service-down", "built-in", "active"},
				{"file-latency", "runtime", "shadowed"},
				{"broken", "runtime", "invalid"},
			}, refs, "in effect by name, then shadowed, then invalid")

			byKey := map[string]strategyItem{}
			for _, it := range got.Strategies {
				byKey[it.Layer+"/"+it.Name] = it
			}

			checkout := byKey["runtime/checkout-latency"]
			require.False(t, checkout.ReadOnly)
			require.Equal(t, "runtime:2", checkout.Version)
			require.Equal(t, 2, checkout.RuntimeVersion)
			require.Equal(t, "Ad", checkout.UpdatedBy)
			require.Equal(t, "Checkout is slow.", checkout.Description)

			generic := byKey["built-in/generic-alert"]
			require.True(t, generic.ReadOnly)
			require.NotEmpty(t, generic.Version)
			require.NotContains(t, generic.Version, "runtime:")

			file := byKey["file/file-latency"]
			require.True(t, file.ReadOnly)
			require.Equal(t, []string{"runtime"}, file.Overrides)

			shadowed := byKey["runtime/file-latency"]
			require.False(t, shadowed.ReadOnly)
			require.Equal(t, 1, shadowed.RuntimeVersion)
			require.NotEmpty(t, shadowed.StatusReason)

			require.NotEmpty(t, byKey["runtime/broken"].StatusReason)
		})
	}

	t.Run("without a session", func(t *testing.T) {
		t.Parallel()

		rec := s.sendRule(t, ruleCaller{}, http.MethodGet, strategiesPath, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestGetStrategy(t *testing.T) {
	t.Parallel()

	s := setupStrategies(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantLayer  string
		wantState  string
		wantBody   string
		wantCode   string
	}{
		{name: "a built-in strategy", path: "generic-alert", wantStatus: http.StatusOK, wantLayer: "built-in", wantState: "active", wantBody: "name: generic-alert"},
		{name: "a runtime strategy", path: "checkout-latency", wantStatus: http.StatusOK, wantLayer: "runtime", wantState: "active", wantBody: "Check what changed."},
		{name: "a strategy file that replaces a runtime one", path: "file-latency", wantStatus: http.StatusOK, wantLayer: "file", wantState: "active", wantBody: "A strategy from the strategies directory."},
		{name: "an invalid runtime strategy", path: "broken", wantStatus: http.StatusOK, wantLayer: "runtime", wantState: "invalid", wantBody: "name: broken"},
		{name: "an unknown name", path: "missing", wantStatus: http.StatusNotFound, wantCode: "not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/"+tt.path, nil)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusOK {
				require.Equal(t, tt.wantCode, ruleProblemOf(t, rec).Code)

				return
			}

			var got strategyItem

			decodeRule(t, rec, &got)
			require.Equal(t, tt.wantLayer, got.Layer)
			require.Equal(t, tt.wantState, got.Status)
			require.Contains(t, got.Body, tt.wantBody)
		})
	}
}

func TestListStrategyVersions(t *testing.T) {
	t.Parallel()

	s := setupStrategies(t)

	type page struct {
		Versions []versionItem `json:"versions"`
		Next     int           `json:"next"`
	}

	get := func(t *testing.T, path string) (*page, int) {
		t.Helper()

		rec := s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+path, nil)
		if rec.Code != http.StatusOK {
			return nil, rec.Code
		}

		var got page

		decodeRule(t, rec, &got)

		return &got, rec.Code
	}

	t.Run("newest first, with the current one marked and no bodies", func(t *testing.T) {
		t.Parallel()

		got, status := get(t, "/checkout-latency/versions")
		require.Equal(t, http.StatusOK, status)
		require.Len(t, got.Versions, 2)
		require.Equal(t, 2, got.Versions[0].Version)
		require.True(t, got.Versions[0].Current)
		require.Equal(t, "Add the first check", got.Versions[0].Reason)
		require.Equal(t, "Ad", got.Versions[0].AuthorName)
		require.Empty(t, got.Versions[0].Body)
		require.Equal(t, 1, got.Versions[1].Version)
		require.False(t, got.Versions[1].Current)
		require.Zero(t, got.Next)
	})

	t.Run("a page at a time", func(t *testing.T) {
		t.Parallel()

		got, _ := get(t, "/checkout-latency/versions?limit=1")
		require.Len(t, got.Versions, 1)
		require.Equal(t, 2, got.Next, "the number to pass as before")

		got, _ = get(t, "/checkout-latency/versions?limit=1&before=2")
		require.Len(t, got.Versions, 1)
		require.Equal(t, 1, got.Versions[0].Version)
		require.Zero(t, got.Next)
	})

	t.Run("a strategy with no runtime version", func(t *testing.T) {
		t.Parallel()

		got, status := get(t, "/generic-alert/versions")
		require.Equal(t, http.StatusOK, status)
		require.Empty(t, got.Versions)
	})

	t.Run("an unknown name", func(t *testing.T) {
		t.Parallel()

		_, status := get(t, "/missing/versions")
		require.Equal(t, http.StatusNotFound, status)
	})

	t.Run("a page size out of range", func(t *testing.T) {
		t.Parallel()

		_, status := get(t, "/checkout-latency/versions?limit=0")
		require.Equal(t, http.StatusBadRequest, status)
	})
}

func TestGetStrategyVersion(t *testing.T) {
	t.Parallel()

	s := setupStrategies(t)

	rec := s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions/1", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var got versionItem

	decodeRule(t, rec, &got)
	require.Equal(t, 1, got.Version)
	require.False(t, got.Current)
	require.Equal(t, "First draft", got.Reason)
	require.Equal(t, "name: checkout-latency\ndescription: Checkout is slow.\n", got.Body)

	rec = s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions/2", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	decodeRule(t, rec, &got)
	require.True(t, got.Current)

	rec = s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions/3", nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "not_found", ruleProblemOf(t, rec).Code)
}

// saveBody is the request body of a save.
type saveBody struct {
	Body        string `json:"body"`
	Reason      string `json:"reason"`
	BaseVersion *int   `json:"baseVersion,omitempty"`
}

func TestSaveStrategyVersion(t *testing.T) {
	t.Parallel()

	const (
		goodBody    = "name: checkout-latency\ndescription: Checkout is very slow.\n"
		currentBody = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n"
	)

	tests := []struct {
		name       string
		caller     ruleCaller
		path       string
		body       saveBody
		wantStatus int
		wantCode   string
		// wantFields maps the refused fields to a part of their message.
		wantFields  map[string]string
		wantVersion int
	}{
		{name: "a new version", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "Clearer"}, wantStatus: http.StatusCreated, wantVersion: 3},
		{name: "a new version by an admin token", caller: adminTokenCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "Clearer"}, wantStatus: http.StatusCreated, wantVersion: 3},
		{
			name: "the version that was read", caller: adminCaller, path: "checkout-latency",
			body: saveBody{Body: goodBody, Reason: "Clearer", BaseVersion: new(2)}, wantStatus: http.StatusCreated, wantVersion: 3,
		},
		{
			name: "a new strategy", caller: adminCaller, path: "disk-full",
			body:       saveBody{Body: "name: disk-full\ndescription: A disk is full.\n", Reason: "New", BaseVersion: new(0)},
			wantStatus: http.StatusCreated, wantVersion: 1,
		},
		{
			name: "a built-in strategy edited as a runtime strategy", caller: adminCaller, path: "generic-alert",
			body:       saveBody{Body: "name: generic-alert\ndescription: Our own.\n", Reason: "Override"},
			wantStatus: http.StatusCreated, wantVersion: 1,
		},
		{
			name: "no reason", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: goodBody},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: map[string]string{"reason": "required"},
		},
		{
			name: "a reason of spaces", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "   "},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: map[string]string{"reason": "required"},
		},
		{
			name: "the paths and lines of the problems", caller: adminCaller, path: "checkout-latency",
			body: saveBody{
				Body:   "name: checkout-latency\ndescription: Slow.\nmatch:\n  textPattern: \"(\"\nbudgets:\n  maxToolCalls: -1\n",
				Reason: "Broken",
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation",
			wantFields: map[string]string{
				"body.match.textPattern":    "(line 4)",
				"body.budgets.maxToolCalls": "(line 6)",
			},
		},
		{
			name: "a body that is not YAML", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: "name: [\n", Reason: "Broken"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: map[string]string{"body": "line"},
		},
		{
			name: "an unknown key", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: goodBody + "colour: red\n", Reason: "Broken"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: map[string]string{"body": `unknown key "colour"`},
		},
		{
			name: "another name in the body", caller: adminCaller, path: "checkout-latency",
			body:       saveBody{Body: "name: other\ndescription: Other.\n", Reason: "Wrong"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation", wantFields: map[string]string{"body.name": `must be "checkout-latency"`},
		},
		{
			name: "the same as the current version", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: currentBody, Reason: "Again"},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "unchanged", wantFields: map[string]string{"body": "same"},
		},
		{
			name: "an old version read", caller: adminCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "Late", BaseVersion: new(1)},
			wantStatus: http.StatusConflict, wantCode: "stale",
		},
		{name: "a viewer", caller: viewerCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "No"}, wantStatus: http.StatusForbidden},
		{name: "a responder", caller: responderCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "No"}, wantStatus: http.StatusForbidden},
		{name: "a viewer token", caller: viewerTokenCaller, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "No"}, wantStatus: http.StatusForbidden},
		{name: "no session", caller: ruleCaller{}, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "No"}, wantStatus: http.StatusUnauthorized},
		{
			name: "an admin without the CSRF token", caller: ruleCaller{session: adSession}, path: "checkout-latency", body: saveBody{Body: goodBody, Reason: "No"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupStrategies(t)
			before := len(s.ruleAudits(t))

			rec := s.sendRule(t, tt.caller, http.MethodPost, strategiesPath+"/"+tt.path+"/versions", tt.body)
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus != http.StatusCreated {
				require.Len(t, s.ruleAudits(t), before, "a refused save writes no audit entry")

				if tt.wantCode != "" {
					p := ruleProblemOf(t, rec)
					require.Equal(t, tt.wantCode, p.Code)

					for field, part := range tt.wantFields {
						require.Contains(t, p.fieldMessages(), field)
						require.Contains(t, p.fieldMessages()[field], part)
					}
				}

				return
			}

			var got versionItem

			decodeRule(t, rec, &got)
			require.Equal(t, tt.wantVersion, got.Version)
			require.True(t, got.Current)
			require.Equal(t, tt.body.Reason, got.Reason)
			require.Equal(t, tt.body.Body, got.Body)

			audits := s.ruleAudits(t)
			require.Len(t, audits, before+1)
			last := audits[len(audits)-1]
			require.Contains(t, []string{strategystore.ActionCreated, strategystore.ActionUpdated}, last.Action)
			require.Equal(t, "strategy:"+tt.path, last.Target)
			require.Contains(t, []string{audit.ActorUser, audit.ActorToken}, last.ActorKind)
			require.Equal(t, tt.body.Reason, last.Details["new"].(map[string]any)["reason"])

			rec = s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/"+tt.path, nil)
			require.Equal(t, http.StatusOK, rec.Code)

			var now strategyItem

			decodeRule(t, rec, &now)
			require.Equal(t, "runtime", now.Layer, "the runtime strategy is in effect")
			require.Equal(t, "runtime:"+itoa(tt.wantVersion), now.Version)
		})
	}
}

func TestSaveStrategyVersionsInARow(t *testing.T) {
	t.Parallel()

	s := setupStrategies(t)

	for i := range 3 {
		rec := s.sendRule(t, adminCaller, http.MethodPost, strategiesPath+"/checkout-latency/versions",
			saveBody{Body: "name: checkout-latency\ndescription: Edit " + itoa(i) + ".\n", Reason: "Edit " + itoa(i)})
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	}

	rec := s.sendRule(t, viewerCaller, http.MethodGet, strategiesPath+"/checkout-latency/versions", nil)

	var got struct {
		Versions []versionItem `json:"versions"`
	}

	decodeRule(t, rec, &got)
	require.Len(t, got.Versions, 5)
	require.Equal(t, 5, got.Versions[0].Version)
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

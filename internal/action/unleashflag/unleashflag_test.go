package unleashflag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		wantErr string
	}{
		{name: "the flag of an exact name", params: `{"project": "default", "environment": "production", "flag": "new-search", "change": "turn_off"}`},
		{name: "a flag that matches a pattern", params: turnOffParams},
		{name: "a flag that matches only the start of a pattern", params: `{"project": "default", "environment": "production", "flag": "checkout-", "change": "turn_off"}`},
		{name: "a flag the pattern does not reach", params: `{"project": "default", "environment": "production", "flag": "checkou", "change": "turn_off"}`, wantErr: `flag "checkou" is not one the action may change`},
		{name: "a flag with a longer name than the exact one", params: `{"project": "default", "environment": "production", "flag": "new-search2", "change": "turn_off"}`, wantErr: `flag "new-search2" is not one the action may change`},
		{name: "a project that is not listed", params: `{"project": "payments", "environment": "production", "flag": "checkout-v2", "change": "turn_off"}`, wantErr: `project "payments" is not one the action may change; allowed: default`},
		{name: "an environment that is not listed", params: `{"project": "default", "environment": "staging", "flag": "checkout-v2", "change": "turn_off"}`, wantErr: `environment "staging" of project default is not one the action may change; allowed: production`},
		{name: "a change that is not allowed", params: `{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "restore_previous"}`, wantErr: `change "restore_previous" is not one of: turn_off`},
		{name: "a flag key with a slash", params: `{"project": "default", "environment": "production", "flag": "checkout-/x", "change": "turn_off"}`, wantErr: `"checkout-/x" is not a project, environment or flag key`},
		{name: "a flag key with a dot-dot", params: `{"project": "default", "environment": "production", "flag": "..", "change": "turn_off"}`, wantErr: `".." is not a project, environment or flag key`},
		{name: "a key that is empty", params: `{"project": "", "environment": "production", "flag": "checkout-v2", "change": "turn_off"}`, wantErr: `"" is not a project, environment or flag key`},
		{name: "an extra parameter", params: `{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "turn_off", "force": true}`, wantErr: `unknown field "force"`},
		{name: "text that is not JSON", params: `turn it off`, wantErr: "parameters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			a := newTestAction(t, api, nil)

			err := a.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}

			require.Empty(t, api.Requests(), "Validate sends nothing")
		})
	}
}

func TestValidateLimitErrorsWrapErrLimit(t *testing.T) {
	t.Parallel()

	a := newTestAction(t, fakehttp.New(t), nil)

	for _, params := range []string{
		`{"project": "payments", "environment": "production", "flag": "checkout-v2", "change": "turn_off"}`,
		`{"project": "default", "environment": "staging", "flag": "checkout-v2", "change": "turn_off"}`,
		`{"project": "default", "environment": "production", "flag": "other", "change": "turn_off"}`,
		`{"project": "default", "environment": "production", "flag": "checkout-v2", "change": "restore_previous"}`,
	} {
		require.ErrorIs(t, a.Validate(t.Context(), json.RawMessage(params)), action.ErrLimit, params)
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	a := newTestAction(t, fakehttp.New(t), nil)
	spec := a.Spec()

	require.Equal(t, "unleash-flag", spec.Name)
	require.Contains(t, spec.Description, "project default, environments production, flags checkout-*, new-search")
	require.Contains(t, spec.Description, "Changes: turn_off.")

	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.ElementsMatch(t, []string{"project", "environment", "flag", "change"}, schema.Required)
	require.Equal(t, []any{"turn_off"}, schema.Properties["change"]["enum"])
}

func TestNewIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   bool
	}{
		{name: "without the opt-in", change: func(*Config) {}, want: true},
		{name: "with the opt-in", change: func(c *Config) { c.AllowReadWrite = true }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestAction(t, fakehttp.New(t), tt.change)

			require.Equal(t, tt.want, action.ReadWriteBlocked(a), "the built action reports it")
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	_, err := New(t.Context(), "unleash-flag", Config{BaseURL: api.URL(), TokenSecret: tokenRef}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)

	newTestAction(t, api, nil)
	require.Empty(t, api.Requests(), "the constructor makes no call")
}

func TestSnapshotTurnOff(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	f.changedElsewhere()
	f.changedElsewhere()

	snap := proposed(t, a, turnOffParams)

	require.Equal(t, "unleash:default/production/checkout-v2", snap.Target)
	require.Equal(t, "101", snap.Version, "the ID of the newest event")

	var before, after state

	require.NoError(t, json.Unmarshal(snap.Before, &before))
	require.NoError(t, json.Unmarshal(snap.After, &after))
	require.True(t, before.Enabled)
	require.False(t, after.Enabled)
	require.True(t, before.sameStrategies(after), "the strategies stay as they are")
	require.Len(t, before.Strategies, 3)
	require.JSONEq(t, `{"rollout": "10", "stickiness": "default", "groupId": "checkout-v2"}`, string(before.Strategies["s1"].Parameters))
	require.JSONEq(t, `[3]`, string(before.Strategies["s1"].Segments))

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{{Path: "enabled", Before: new("true"), After: new("false")}}, changes)

	require.Empty(t, f.written(), "proposing writes nothing")

	for _, req := range api.Requests() {
		require.Equal(t, http.MethodGet, req.Method)
		require.Equal(t, []string{testToken}, req.Header["Authorization"], "the token is sent as it is, with no prefix and no white space")
		require.True(t, strings.HasPrefix(req.Header.Get("User-Agent"), "sidekick/"))
	}
}

func TestSnapshotReadsTheNewestEventOfTheFlagInTheEnvironment(t *testing.T) {
	t.Parallel()

	api, _ := newFakeUnleash(t)
	a := newTestAction(t, api, nil)

	snap := proposed(t, a, turnOffParams)
	require.Equal(t, "0", snap.Version, "a flag with no events has version 0")

	var query map[string][]string

	for _, req := range api.Requests() {
		if req.Path == "/api/admin/search/events" {
			query = req.Query
		}
	}

	require.Equal(t, map[string][]string{
		"project": {"IS:default"}, "environment": {"IS:production"}, "feature": {"IS:checkout-v2"}, "limit": {"1"},
	}, query)
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(f *fakeUnleash)
		params  string
		wantErr string
		// wantNoRequest says the refusal comes before anything is sent.
		wantNoRequest bool
	}{
		{name: "a flag that is already off", setup: func(f *fakeUnleash) { f.enabled = false }, params: turnOffParams, wantErr: "already off"},
		{name: "a flag that is archived or missing", setup: func(f *fakeUnleash) { f.archived = true }, params: turnOffParams, wantErr: "does not exist in project default, or it is archived"},
		{name: "a flag without the environment", setup: func(f *fakeUnleash) { f.noEnvironment = true }, params: turnOffParams, wantErr: "has no environment production"},
		{
			name: "a flag outside the limits", params: `{"project": "default", "environment": "production", "flag": "payments-v1", "change": "turn_off"}`,
			wantErr: "payments-v1", wantNoRequest: true,
		},
		{
			name: "an environment outside the limits", params: `{"project": "default", "environment": "staging", "flag": "checkout-v2", "change": "turn_off"}`,
			wantErr: "staging", wantNoRequest: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			if tt.setup != nil {
				tt.setup(f)
			}

			a := newTestAction(t, api, nil)

			_, err := a.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantNoRequest {
				require.Empty(t, api.Requests())
			}

			require.Empty(t, f.written())
		})
	}
}

func TestSnapshotFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		route         string
		status        int
		wantPermanent bool
	}{
		{name: "the flag read is refused", route: "GET flag", status: http.StatusForbidden, wantPermanent: true},
		{name: "the flag read fails", route: "GET flag", status: http.StatusBadGateway},
		{name: "the strategies read fails", route: "GET strategies", status: http.StatusInternalServerError},
		{name: "the events read is refused", route: "GET events", status: http.StatusUnauthorized, wantPermanent: true},
		{name: "the events read is rate limited", route: "GET events", status: http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			f.fail(tt.route, tt.status, `{"name": "Failure", "message": "no"}`)

			a := newTestAction(t, api, nil)

			_, err := a.Snapshot(t.Context(), json.RawMessage(turnOffParams))
			require.Error(t, err)
			require.NotErrorIs(t, err, action.ErrLimit)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
		})
	}
}

func TestExecuteTurnOff(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	f.changedElsewhere()

	snap := proposed(t, a, turnOffParams)

	res, err := a.Execute(t.Context(), invocationOf(turnOffParams, &snap))
	require.NoError(t, err)

	require.False(t, f.isEnabled())
	require.Equal(t, []string{"off"}, f.written())
	require.Equal(t, "Turned flag checkout-v2 off in production of project default.", res.Summary)
	require.Equal(t, []action.Link{{Title: "Flag checkout-v2", URL: api.URL() + "/projects/default/features/checkout-v2"}}, res.Links)

	var posts []fakehttp.Request

	for _, req := range api.Requests() {
		if req.Method == http.MethodPost {
			posts = append(posts, req)
		}
	}

	require.Len(t, posts, 1)
	require.Equal(t, testEnvPath+"/off", posts[0].Path)
	require.Equal(t, []string{testToken}, posts[0].Header["Authorization"])
	require.Empty(t, posts[0].Body)
}

func TestExecuteTwiceTurnsTheFlagOffOnce(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	snap := proposed(t, a, turnOffParams)
	inv := invocationOf(turnOffParams, &snap)

	first, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), inv)
	require.NoError(t, err, "a retry after a crash finds the work done")
	require.Equal(t, first, second)
	require.Equal(t, []string{"off"}, f.written(), "the flag was turned off once")
}

func TestExecuteWhenTheFlagWasTurnedOffByAnotherPerson(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	snap := proposed(t, a, turnOffParams)

	f.mu.Lock()
	f.enabled = false
	f.addEvent()
	f.mu.Unlock()

	_, err := a.Execute(t.Context(), invocationOf(turnOffParams, &snap))
	require.NoError(t, err, "the flag has what the snapshot writes")
	require.Empty(t, f.written(), "nothing is written for it")
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after func(f *fakeUnleash)
	}{
		{name: "someone changed the flag after the proposal", after: func(f *fakeUnleash) { f.changedElsewhere() }},
		{name: "a strategy changed without an event", after: func(f *fakeUnleash) {
			f.mu.Lock()
			defer f.mu.Unlock()

			f.strategies[0]["parameters"] = map[string]any{"rollout": "50", "stickiness": "default", "groupId": "checkout-v2"}
		}},
		{name: "a strategy was removed without an event", after: func(f *fakeUnleash) {
			f.mu.Lock()
			defer f.mu.Unlock()

			f.strategies = f.strategies[:2]
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			a := newTestAction(t, api, nil)
			snap := proposed(t, a, turnOffParams)

			tt.after(f)

			_, err := a.Execute(t.Context(), invocationOf(turnOffParams, &snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.written())
		})
	}
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		body          string
		wantPermanent bool
		wantText      string
	}{
		{name: "Unleash fails", status: http.StatusInternalServerError, body: `{"name": "InternalError", "message": "boom"}`},
		{name: "Unleash is rate limiting", status: http.StatusTooManyRequests, body: `{}`},
		{name: "a gateway fails", status: http.StatusBadGateway, body: `no`},
		{
			name: "the environment uses change requests", status: http.StatusForbidden, wantPermanent: true,
			body:     `{"name": "PermissionError", "message": "You do not have SKIP_CHANGE_REQUEST", "permissions": ["SKIP_CHANGE_REQUEST"]}`,
			wantText: "this environment uses change requests, and Unleash refuses direct changes to it",
		},
		{
			name: "the token lacks the permission", status: http.StatusForbidden, wantPermanent: true,
			body:     `{"name": "PermissionError", "message": "You need UPDATE_FEATURE_ENVIRONMENT", "permissions": ["UPDATE_FEATURE_ENVIRONMENT"]}`,
			wantText: "UPDATE_FEATURE_ENVIRONMENT",
		},
		{
			name: "the flag was archived", status: http.StatusBadRequest, wantPermanent: true, wantText: "archived",
			body: `{"name": "ArchivedFeatureError", "message": "Cannot perform this operation on archived features"}`,
		},
		{name: "the token is refused", status: http.StatusUnauthorized, body: `{}`, wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			a := newTestAction(t, api, nil)
			snap := proposed(t, a, turnOffParams)

			f.fail("POST off", tt.status, tt.body)

			_, err := a.Execute(t.Context(), invocationOf(turnOffParams, &snap))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotErrorIs(t, err, action.ErrConflict)

			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}

			require.True(t, f.isEnabled())
		})
	}
}

func TestExecuteRefusesWhatTheLimitsNoLongerAllow(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	snap := proposed(t, a, turnOffParams)

	requests := len(api.Requests())
	tight := newTestAction(t, api, func(c *Config) { c.Limits.Projects[0].Flags = []string{"new-search"} })

	_, err := tight.Execute(t.Context(), invocationOf(turnOffParams, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Len(t, api.Requests(), requests, "nothing is sent")
	require.Empty(t, f.written())
}

func TestExecuteRefusesABadSnapshot(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	snap := proposed(t, a, turnOffParams)

	other := snap
	other.Target = "unleash:default/production/other"

	wrongState := snap
	wrongState.After = json.RawMessage(`{"enabled": true, "strategies": {}}`)

	broken := snap
	broken.Before = json.RawMessage(`[`)

	tests := []struct {
		name string
		snap *action.Snapshot
	}{
		{name: "no snapshot", snap: nil},
		{name: "the snapshot of another flag", snap: &other},
		{name: "a snapshot that changes more than one thing", snap: &wrongState},
		{name: "a snapshot that does not decode", snap: &broken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := a.Execute(t.Context(), invocationOf(turnOffParams, tt.snap))
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, f.written())
		})
	}
}

func TestExecuteWhenTheFlagIsGone(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	snap := proposed(t, a, turnOffParams)

	f.mu.Lock()
	f.archived = true
	f.mu.Unlock()

	_, err := a.Execute(t.Context(), invocationOf(turnOffParams, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, f.written())
}

func TestAnEmptyTokenIsAnError(t *testing.T) {
	t.Parallel()

	api, _ := newFakeUnleash(t)

	a, err := New(t.Context(), "unleash-flag", Config{
		BaseURL: api.URL(), TokenSecret: tokenRef,
		Limits: Limits{Projects: []Project{{Key: testProject, Environments: []string{testEnv}, Flags: []string{"*"}}}},
	}, registry.Deps{Secrets: emptySecrets{}, HTTP: api.Client()})
	require.NoError(t, err)

	_, err = a.(*Flag).Snapshot(t.Context(), json.RawMessage(turnOffParams))
	require.ErrorIs(t, err, errEmptyToken)
	require.Empty(t, api.Requests())
}

// emptySecrets resolves every secret to nothing but white space.
type emptySecrets struct{}

func (emptySecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(" \n"), nil
}

// isPermanent reports whether Sidekick ends the proposal at once on err.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent) || errors.Is(err, action.ErrLimit) || errors.Is(err, action.ErrConflict)
}

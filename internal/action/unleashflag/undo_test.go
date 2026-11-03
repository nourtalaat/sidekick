package unleashflag

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// executed proposes and runs params, and returns what an undo needs.
func executed(t *testing.T, a *Flag, params string) (action.Invocation, action.Snapshot) {
	t.Helper()

	snap := proposed(t, a, params)
	inv := invocationOf(params, &snap)

	_, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	return inv, snap
}

func TestUndo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		prepare func(f *fakeUnleash)
		setup   func(f *fakeUnleash)
	}{
		{name: "a turn_off", params: turnOffParams, setup: func(*fakeUnleash) {}},
		{name: "a strategy that was changed", params: restoreParams, setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) }},
		{name: "a strategy that was added", params: restoreParams, setup: func(f *fakeUnleash) { f.externalAdd(anotherStrategy()) }},
		{name: "a strategy that was removed", params: restoreParams, setup: func(f *fakeUnleash) { f.externalRemove("s2") }},
		{name: "the flag was turned on", params: restoreParams, prepare: func(f *fakeUnleash) { f.enabled = false }, setup: func(f *fakeUnleash) { f.externalToggle(true) }},
		{name: "the flag was turned off", params: restoreParams, setup: func(f *fakeUnleash) { f.externalToggle(false) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			if tt.prepare != nil {
				tt.prepare(f)
			}

			tt.setup(f)

			a := newTestAction(t, api, allowRestore)
			beforeExecute := f.state(t)

			inv, snap := executed(t, a, tt.params)
			written := len(f.written())

			res, err := a.Undo(t.Context(), inv, snap, false)
			require.NoError(t, err)
			require.Contains(t, res.Summary, "Undid the change: ")
			require.Len(t, f.written(), written+1, "one call")
			requireSameModuloIDs(t, beforeExecute, f.state(t))

			again, err := a.Undo(t.Context(), inv, snap, false)
			require.NoError(t, err, "an undo can be repeated")
			require.Contains(t, again.Summary, "already has what it had before the change")
			require.Len(t, f.written(), written+1, "nothing more is written")
		})
	}
}

func TestUndoConflictAndForce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// setup is run before the change; edit after it.
		setup         func(f *fakeUnleash)
		edit          func(f *fakeUnleash)
		wantLive      string
		wantForced    string
		wantForcedErr error
		wantBody      string
	}{
		{
			name:  "the strategy was changed again",
			setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			edit: func(f *fakeUnleash) {
				f.externalUpdate("s1", func(m map[string]any) { m["parameters"] = rolloutParameters("50") })
			},
			wantLive:   `"50"`,
			wantForced: "update s1",
			wantBody:   `"rollout":"100"`,
		},
		{
			name:       "the strategy that was added was changed",
			setup:      func(f *fakeUnleash) { f.externalRemove("s2") },
			edit:       func(f *fakeUnleash) { f.externalUpdate("n1", func(m map[string]any) { m["title"] = "Everyone" }) },
			wantLive:   `"Everyone"`,
			wantForced: "delete n1",
		},
		{
			name:          "the strategy is gone",
			setup:         func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			edit:          func(f *fakeUnleash) { f.externalRemove("s1") },
			wantForcedErr: errStrategyGone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			tt.setup(f)

			a := newTestAction(t, api, allowRestore)
			inv, snap := executed(t, a, restoreParams)
			written := len(f.written())

			tt.edit(f)

			_, err := a.Undo(t.Context(), inv, snap, false)
			require.ErrorIs(t, err, action.ErrConflict)
			require.Len(t, f.written(), written, "nothing is written")

			var conflict *action.ConflictError

			require.ErrorAs(t, err, &conflict)

			var live state

			require.NoError(t, json.Unmarshal(conflict.Live, &live), "the conflict holds the state of the flag")
			require.Equal(t, f.state(t).Strategies["s1"].Parameters, live.Strategies["s1"].Parameters)

			if tt.wantLive != "" {
				require.Contains(t, string(conflict.Live), tt.wantLive)
			}

			_, err = a.Undo(t.Context(), inv, snap, true)
			if tt.wantForcedErr != nil {
				require.ErrorIs(t, err, tt.wantForcedErr)
				require.ErrorIs(t, err, action.ErrPermanent)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantForced, f.written()[written], "a forced undo makes the call")

			if tt.wantBody != "" {
				bodies := f.writtenBodies()
				require.Contains(t, string(bodies[len(bodies)-1]), tt.wantBody)
			}
		})
	}
}

func TestUndoOfAnAddedStrategyFindsTheNewOne(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		edit       func(f *fakeUnleash)
		wantWrites []string
		wantErr    error
	}{
		{
			name: "another strategy was added since", edit: func(f *fakeUnleash) { f.externalAdd(anotherStrategy()) },
			wantWrites: []string{"delete n1"},
		},
		{
			name: "another strategy with the same values was added since",
			edit: func(f *fakeUnleash) {
				added := f.strategy("n1")
				f.externalAdd(added)
			},
			wantErr: errStrategyMany,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			f.externalRemove("s2")

			a := newTestAction(t, api, allowRestore)
			inv, snap := executed(t, a, restoreParams)
			written := len(f.written())

			tt.edit(f)

			_, err := a.Undo(t.Context(), inv, snap, false)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrPermanent)
				require.Len(t, f.written(), written)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantWrites, f.written()[written:])
			require.NotNil(t, f.strategy("n2"), "the other strategy stays")
		})
	}
}

func TestUndoWillNotTurnOnAFlagWithoutAnEnabledStrategy(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)
	inv, snap := executed(t, a, turnOffParams)

	f.edit(func(f *fakeUnleash) {
		for _, s := range f.strategies {
			s["disabled"] = true
		}
	})

	_, err := a.Undo(t.Context(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, errNoEnabledStrategy)
	require.Equal(t, []string{"off"}, f.written())
}

func TestUndoFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		body          string
		wantPermanent bool
		wantText      string
	}{
		{name: "Unleash fails", status: http.StatusInternalServerError, body: `{}`},
		{
			name: "the environment uses change requests", status: http.StatusForbidden, wantPermanent: true,
			body: `{"name": "PermissionError", "message": "no", "permissions": ["SKIP_CHANGE_REQUEST"]}`, wantText: "this environment uses change requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			a := newTestAction(t, api, nil)
			inv, snap := executed(t, a, turnOffParams)

			f.fail("POST on", tt.status, tt.body)

			_, err := a.Undo(t.Context(), inv, snap, false)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))

			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}

			require.False(t, f.isEnabled())
		})
	}
}

func TestUndoNeedsTheFlagInTheLimitsButNotTheChange(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	f.externalUpdate("s1", changeRollout)

	allowed := newTestAction(t, api, allowRestore)
	inv, snap := executed(t, allowed, restoreParams)

	onlyTurnOff := newTestAction(t, api, nil)

	_, err := onlyTurnOff.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err, "limits.allow does not stop an undo")

	requests := len(api.Requests())
	tight := newTestAction(t, api, func(c *Config) { c.Limits.Projects[0].Flags = []string{"new-search"} })

	_, err = tight.Undo(t.Context(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrLimit)
	require.Len(t, api.Requests(), requests, "nothing is sent")
}

func TestUndoWithoutASnapshot(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	a := newTestAction(t, api, nil)

	_, err := a.Undo(t.Context(), invocationOf(turnOffParams, nil), action.Snapshot{}, false)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, errNoSnapshot)
	require.Empty(t, f.written())
}

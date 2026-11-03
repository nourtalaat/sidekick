package unleashflag

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// rolloutParameters are the parameters of the first strategy, with a
// rollout.
func rolloutParameters(rollout string) map[string]any {
	return map[string]any{"rollout": rollout, "stickiness": "default", "groupId": "checkout-v2"}
}

// anotherStrategy is a strategy that tests add.
func anotherStrategy() map[string]any {
	return map[string]any{
		"name": "default", "title": nil, "disabled": false, "sortOrder": 3,
		"constraints": []any{}, "parameters": map[string]any{}, "variants": []any{}, "segments": []any{},
	}
}

// changeRollout is a change of the first strategy, which another person
// makes.
func changeRollout(m map[string]any) { m["parameters"] = rolloutParameters("100") }

func TestSnapshotRestorePrevious(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(f *fakeUnleash)
		want  []action.Change
		// For a change that adds or removes a strategy, the path of the
		// strategy, and which side of the difference has no value.
		wantPath          string
		wantBeforeMissing bool
		wantAfterMissing  bool
	}{
		{
			name:  "a strategy that was changed",
			setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			want:  []action.Change{{Path: "strategies.s1.parameters.rollout", Before: new(`"100"`), After: new(`"10"`)}},
		},
		{
			name:  "the flag was turned on",
			setup: func(f *fakeUnleash) { f.externalToggle(true) },
			want:  []action.Change{{Path: "enabled", Before: new("true"), After: new("false")}},
		},
		{
			name:  "the flag was turned off",
			setup: func(f *fakeUnleash) { f.externalToggle(false) },
			want:  []action.Change{{Path: "enabled", Before: new("false"), After: new("true")}},
		},
		{name: "a strategy that was added", setup: func(f *fakeUnleash) { f.externalAdd(anotherStrategy()) }, wantPath: "strategies.n1", wantAfterMissing: true},
		{name: "a strategy that was removed", setup: func(f *fakeUnleash) { f.externalRemove("s2") }, wantPath: "strategies.new", wantBeforeMissing: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			tt.setup(f)

			a := newTestAction(t, api, allowRestore)
			snap := proposed(t, a, restoreParams)

			changes, err := action.Changes(snap)
			require.NoError(t, err)

			if tt.wantPath == "" {
				require.Equal(t, tt.want, changes)
			} else {
				require.Len(t, changes, 1)
				require.Equal(t, tt.wantPath, changes[0].Path)
				require.Equal(t, tt.wantBeforeMissing, changes[0].Before == nil)
				require.Equal(t, tt.wantAfterMissing, changes[0].After == nil)
			}

			require.Equal(t, "unleash:default/production/checkout-v2", snap.Target)
			require.NotEqual(t, "0", snap.Version, "the version is the ID of the newest event")
			require.Empty(t, f.written(), "proposing writes nothing")
		})
	}
}

func TestSnapshotRestorePreviousRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(f *fakeUnleash)
		wantErr string
	}{
		{name: "no change to restore", setup: func(*fakeUnleash) {}, wantErr: "no change in this environment to restore"},
		{
			name: "a reordering of the strategies", setup: func(f *fakeUnleash) { f.externalEvent("strategy-order-changed", "") },
			wantErr: `of type "strategy-order-changed", which Sidekick cannot restore`,
		},
		{
			name: "a change that Sidekick does not restore", setup: func(f *fakeUnleash) { f.changedElsewhere() },
			wantErr: `of type "feature-environment-variants-updated", which Sidekick cannot restore`,
		},
		{
			name: "a change in a group of changes", setup: func(f *fakeUnleash) { f.externalEvent("feature-strategy-add", "01HXGROUP") },
			wantErr: "one of a group of changes made together",
		},
		{
			name: "a flag with a release plan", wantErr: "belongs to a release plan",
			setup: func(f *fakeUnleash) {
				f.externalToggle(false)
				f.edit(func(f *fakeUnleash) { f.strategies[1]["milestoneId"] = "m1" })
			},
		},
		{
			name: "a strategy that already has its earlier values", wantErr: "already has the values it had before",
			setup: func(f *fakeUnleash) {
				f.externalUpdate("s1", changeRollout)
				f.edit(func(f *fakeUnleash) { f.strategies[0]["parameters"] = rolloutParameters("10") })
			},
		},
		{
			name: "a strategy that was changed and is gone", wantErr: "the strategy the newest change updated is not in this environment",
			setup: func(f *fakeUnleash) {
				f.externalUpdate("s1", changeRollout)
				f.edit(func(f *fakeUnleash) { f.strategies = f.strategies[1:] })
			},
		},
		{
			name: "a strategy that was added and is gone", wantErr: "the strategy the newest change added is not in this environment",
			setup: func(f *fakeUnleash) {
				f.externalAdd(anotherStrategy())
				f.edit(func(f *fakeUnleash) { f.strategies = f.strategies[:3] })
			},
		},
		{
			name: "a strategy that was removed and is back", wantErr: "already has a strategy with the values of the one",
			setup: func(f *fakeUnleash) {
				removed := f.strategy("s2")
				f.externalRemove("s2")
				f.edit(func(f *fakeUnleash) { f.strategies = append(f.strategies, removed) })
			},
		},
		{
			name: "the flag was turned on and is already off", wantErr: "it is already off in this environment",
			setup: func(f *fakeUnleash) {
				f.externalToggle(true)
				f.edit(func(f *fakeUnleash) { f.enabled = false })
			},
		},
		{
			name: "the flag was turned off and is already on", wantErr: "it is already on in this environment",
			setup: func(f *fakeUnleash) {
				f.externalToggle(false)
				f.edit(func(f *fakeUnleash) { f.enabled = true })
			},
		},
		{
			name: "a strategy that was enabled again, which would leave none enabled", wantErr: "no enabled strategy",
			setup: func(f *fakeUnleash) {
				f.edit(func(f *fakeUnleash) {
					f.strategies = f.strategies[:1]
					f.strategies[0]["disabled"] = true
				})
				f.externalUpdate("s1", func(m map[string]any) { m["disabled"] = false })
			},
		},
		{
			name: "the flag was turned off and no strategy is enabled", wantErr: "no enabled strategy",
			setup: func(f *fakeUnleash) {
				f.edit(func(f *fakeUnleash) { f.strategies = f.strategies[2:] })
				f.externalToggle(false)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			tt.setup(f)

			a := newTestAction(t, api, allowRestore)

			_, err := a.Snapshot(t.Context(), json.RawMessage(restoreParams))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, f.written())
		})
	}
}

func TestSnapshotRestorePreviousWithoutTheChangeInTheLimits(t *testing.T) {
	t.Parallel()

	api, _ := newFakeUnleash(t)
	a := newTestAction(t, api, nil)

	_, err := a.Snapshot(t.Context(), json.RawMessage(restoreParams))
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, `change "restore_previous" is not one of: turn_off`)
	require.Empty(t, api.Requests(), "nothing is sent")
}

func TestExecuteRestorePrevious(t *testing.T) {
	t.Parallel()

	rollout10 := `{"name": "flexibleRollout", "title": "Gradual rollout", "disabled": false, "sortOrder": 0,
		"constraints": [{"contextName": "userId", "operator": "IN", "values": ["a", "b"], "inverted": false, "caseInsensitive": false}],
		"parameters": {"rollout": "10", "stickiness": "default", "groupId": "checkout-v2"}, "variants": [], "segments": [3]}`
	defaultStrategy := `{"name": "default", "title": null, "disabled": false, "sortOrder": 1, "constraints": [], "parameters": {}, "variants": [], "segments": []}`

	tests := []struct {
		name string
		// prepare sets the flag up as it is before the change that is restored.
		prepare     func(f *fakeUnleash)
		setup       func(f *fakeUnleash)
		wantWrites  []string
		wantBody    string
		wantSummary string
	}{
		{
			name: "a strategy that was changed", setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			wantWrites: []string{"update s1"}, wantBody: rollout10,
			wantSummary: "Changed strategy s1 of flag checkout-v2 in production of project default.",
		},
		{
			name: "a strategy that was added", setup: func(f *fakeUnleash) { f.externalAdd(anotherStrategy()) },
			wantWrites: []string{"delete n1"}, wantSummary: "Removed strategy n1 from flag checkout-v2 in production of project default.",
		},
		{
			name: "a strategy that was removed", setup: func(f *fakeUnleash) { f.externalRemove("s2") },
			wantWrites: []string{"create"}, wantBody: defaultStrategy,
			wantSummary: "Added a strategy to flag checkout-v2 in production of project default.",
		},
		{
			name: "the flag was turned on", prepare: func(f *fakeUnleash) { f.enabled = false }, setup: func(f *fakeUnleash) { f.externalToggle(true) },
			wantWrites: []string{"off"}, wantSummary: "Turned flag checkout-v2 off in production of project default.",
		},
		{
			name: "the flag was turned off", setup: func(f *fakeUnleash) { f.externalToggle(false) },
			wantWrites: []string{"on"}, wantSummary: "Turned flag checkout-v2 on in production of project default.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			if tt.prepare != nil {
				tt.prepare(f)
			}

			before := f.state(t)
			tt.setup(f)

			a := newTestAction(t, api, allowRestore)
			snap := proposed(t, a, restoreParams)
			inv := invocationOf(restoreParams, &snap)

			res, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)
			require.Equal(t, tt.wantSummary, res.Summary)
			require.Equal(t, tt.wantWrites, f.written(), "one call")

			if tt.wantBody != "" {
				bodies := f.writtenBodies()
				require.Len(t, bodies, 1)
				require.JSONEq(t, tt.wantBody, string(bodies[0]), "what Unleash had before the change is what is sent")
			}

			requireSameModuloIDs(t, before, f.state(t))

			again, err := a.Execute(t.Context(), inv)
			require.NoError(t, err, "a retry after a crash finds the work done")
			require.Equal(t, res, again)
			require.Equal(t, tt.wantWrites, f.written(), "nothing more is written")
		})
	}
}

func TestExecuteRestorePreviousConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after func(f *fakeUnleash)
	}{
		{name: "someone changed the flag after the proposal", after: func(f *fakeUnleash) { f.externalUpdate("s2", func(m map[string]any) { m["title"] = "Everyone" }) }},
		{name: "another strategy changed without an event", after: func(f *fakeUnleash) {
			f.edit(func(f *fakeUnleash) { f.strategies[2]["parameters"] = map[string]any{"userIds": "z"} })
		}},
		{name: "the strategy to restore changed again", after: func(f *fakeUnleash) {
			f.externalUpdate("s1", func(m map[string]any) { m["parameters"] = rolloutParameters("50") })
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			f.externalUpdate("s1", changeRollout)

			a := newTestAction(t, api, allowRestore)
			snap := proposed(t, a, restoreParams)

			tt.after(f)

			_, err := a.Execute(t.Context(), invocationOf(restoreParams, &snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.written())
		})
	}
}

func TestExecuteRestorePreviousFailures(t *testing.T) {
	t.Parallel()

	changeRequests := `{"name": "PermissionError", "message": "no", "permissions": ["SKIP_CHANGE_REQUEST"]}`

	tests := []struct {
		name          string
		setup         func(f *fakeUnleash)
		route         string
		status        int
		body          string
		wantPermanent bool
		wantText      string
	}{
		{
			name: "the update fails", setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			route: "PUT update", status: http.StatusInternalServerError, body: `{}`,
		},
		{
			name: "the update is refused because a segment is gone", setup: func(f *fakeUnleash) { f.externalUpdate("s1", changeRollout) },
			route: "PUT update", status: http.StatusBadRequest, body: `{"name": "BadDataError", "message": "segment 3 does not exist"}`,
			wantPermanent: true, wantText: "segment 3 does not exist",
		},
		{
			name: "the add is refused because the environment uses change requests", setup: func(f *fakeUnleash) { f.externalRemove("s2") },
			route: "POST create", status: http.StatusForbidden, body: changeRequests,
			wantPermanent: true, wantText: "this environment uses change requests",
		},
		{
			name: "the add is rate limited", setup: func(f *fakeUnleash) { f.externalRemove("s2") },
			route: "POST create", status: http.StatusTooManyRequests, body: `{}`,
		},
		{
			name: "the delete is refused because the token lacks the permission", setup: func(f *fakeUnleash) { f.externalAdd(anotherStrategy()) },
			route: "DELETE delete", status: http.StatusForbidden, wantPermanent: true, wantText: "DELETE_FEATURE_STRATEGY",
			body: `{"name": "PermissionError", "message": "You need DELETE_FEATURE_STRATEGY", "permissions": ["DELETE_FEATURE_STRATEGY"]}`,
		},
		{
			name: "the toggle is refused because the environment uses change requests", setup: func(f *fakeUnleash) { f.externalToggle(false) },
			route: "POST on", status: http.StatusForbidden, body: changeRequests,
			wantPermanent: true, wantText: "this environment uses change requests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeUnleash(t)
			tt.setup(f)

			a := newTestAction(t, api, allowRestore)
			snap := proposed(t, a, restoreParams)

			f.fail(tt.route, tt.status, tt.body)

			_, err := a.Execute(t.Context(), invocationOf(restoreParams, &snap))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, isPermanent(err))
			require.NotErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.written(), "the call did not change the flag")

			if tt.wantText != "" {
				require.ErrorContains(t, err, tt.wantText)
			}
		})
	}
}

func TestExecuteWillNotTurnOnAFlagWithoutAnEnabledStrategy(t *testing.T) {
	t.Parallel()

	api, f := newFakeUnleash(t)
	f.edit(func(f *fakeUnleash) {
		f.strategies = f.strategies[2:]
		f.enabled = false
	})

	a := newTestAction(t, api, allowRestore)
	live := f.state(t)

	on := live.cloned()
	on.Enabled = true

	before, err := json.Marshal(live)
	require.NoError(t, err)

	after, err := json.Marshal(on)
	require.NoError(t, err)

	snap := action.Snapshot{Target: "unleash:default/production/checkout-v2", Before: before, After: after, Version: "0"}

	_, err = a.Execute(t.Context(), invocationOf(restoreParams, &snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorIs(t, err, errNoEnabledStrategy)
	require.Empty(t, f.written())
}

func TestDiffStep(t *testing.T) {
	t.Parallel()

	one := normalisedStrategy(t, strategy{Name: "default"})
	two := normalisedStrategy(t, strategy{Name: "flexibleRollout", Parameters: json.RawMessage(`{"rollout":"5"}`)})
	changed := normalisedStrategy(t, strategy{Name: "default", Disabled: true})

	tests := []struct {
		name    string
		before  state
		after   state
		want    step
		wantErr bool
	}{
		{name: "turn off", before: state{Enabled: true}, after: state{}, want: step{kind: stepOff}},
		{name: "turn on", before: state{}, after: state{Enabled: true}, want: step{kind: stepOn}},
		{
			name:   "add a strategy",
			before: state{Strategies: map[string]strategy{"a": one}}, after: state{Strategies: map[string]strategy{"a": one, newStrategyKey: two}},
			want: step{kind: stepCreate, id: newStrategyKey, to: two},
		},
		{
			name:   "remove a strategy",
			before: state{Strategies: map[string]strategy{"a": one, "b": two}}, after: state{Strategies: map[string]strategy{"a": one}},
			want: step{kind: stepDelete, id: "b", from: two},
		},
		{
			name:   "change a strategy",
			before: state{Strategies: map[string]strategy{"a": one}}, after: state{Strategies: map[string]strategy{"a": changed}},
			want: step{kind: stepUpdate, id: "a", from: one, to: changed},
		},
		{name: "nothing changes", before: state{Strategies: map[string]strategy{"a": one}}, after: state{Strategies: map[string]strategy{"a": one}}, wantErr: true},
		{
			name:   "two strategies change",
			before: state{Strategies: map[string]strategy{"a": one, "b": two}}, after: state{Strategies: map[string]strategy{"a": changed}},
			wantErr: true,
		},
		{
			name:   "the flag is turned on and a strategy changes",
			before: state{Strategies: map[string]strategy{"a": one}}, after: state{Enabled: true, Strategies: map[string]strategy{"a": changed}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := diffStep(tt.before, tt.after)
			if tt.wantErr {
				require.ErrorIs(t, err, errNotOneStep)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// normalisedStrategy returns the strategy in its fixed form.
func normalisedStrategy(t *testing.T, s strategy) strategy {
	t.Helper()

	out, err := s.normalised()
	require.NoError(t, err)

	return out
}

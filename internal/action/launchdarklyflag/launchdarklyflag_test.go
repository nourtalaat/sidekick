package launchdarklyflag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	turnOffPatch = `{"comment":"Sidekick proposal p1","environmentKey":"production","instructions":[{"kind":"turnFlagOff"}]}`
	turnOnPatch  = `{"comment":"Sidekick undo of proposal p1","environmentKey":"production","instructions":[{"kind":"turnFlagOn"}]}`
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "a proposal inside the limits", params: paramsJSON},
		{name: "a flag the pattern matches", params: `{"project":"shop","environment":"production","flag":"checkout-","change":"turn_off"}`},
		{name: "a project outside the limits", params: `{"project":"other","environment":"production","flag":"checkout-new","change":"turn_off"}`, wantErr: "allowed: shop", wantLimit: true},
		{name: "an environment outside the limits", params: `{"project":"shop","environment":"test","flag":"checkout-new","change":"turn_off"}`, wantErr: "allowed: production", wantLimit: true},
		{name: "a flag the patterns do not match", params: `{"project":"shop","environment":"production","flag":"search-new","change":"turn_off"}`, wantErr: "allowed flags: checkout-*", wantLimit: true},
		{name: "a flag that is a path", params: `{"project":"shop","environment":"production","flag":"checkout-/../x","change":"turn_off"}`, wantErr: "is not allowed", wantLimit: true},
		{name: "a change that is not supported", params: `{"project":"shop","environment":"production","flag":"checkout-new","change":"restore_previous"}`, wantErr: "is not supported"},
		{name: "a key the action does not have", params: `{"project":"shop","environment":"production","flag":"checkout-new","change":"turn_off","x":1}`, wantErr: "decode the parameters"},
		{name: "no parameters", params: `{}`, wantErr: "is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			a := buildAction(t, api, limitedConfig(api))

			err := a.Validate(context.Background(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			}

			require.Empty(t, api.Requests(), "Validate makes no call")
		})
	}
}

func TestSpecListsTheLimits(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	spec := buildAction(t, api, limitedConfig(api)).Spec()

	require.Equal(t, "flag-off", spec.Name)
	require.Contains(t, spec.Description, "project shop, environments production, flags checkout-*")
	require.JSONEq(t, string(paramsSchema), string(spec.ParamsSchema))
}

func TestApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allow   bool
		blocked bool
	}{
		{name: "without the opt-in", allow: false, blocked: true},
		{name: "with the opt-in", allow: true, blocked: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			cfg := limitedConfig(api)
			cfg.AllowReadWrite = tt.allow

			require.Equal(t, tt.blocked, action.ReadWriteBlocked(&cfg))
			require.Equal(t, tt.blocked, action.ReadWriteBlocked(buildAction(t, api, cfg)))
		})
	}
}

func TestNewNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), "flag-off", validConfig(), registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, _ := newFake(t)
	snap := snapshotOf(t, buildAction(t, api, limitedConfig(api)))

	require.Equal(t, "launchdarkly:proj/shop/env/production/flag/checkout-new", snap.Target)
	require.JSONEq(t, `{"on":true}`, string(snap.Before))
	require.JSONEq(t, `{"on":false}`, string(snap.After))
	require.Equal(t, "7", snap.Version)

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Len(t, changes, 1, "only the on setting changes")

	for _, r := range api.Requests() {
		require.Equal(t, http.MethodGet, r.Method, "Snapshot changes nothing")
		require.Equal(t, "fake-token", r.Header.Get("Authorization"))
		require.Equal(t, apiVersion, r.Header.Get("LD-API-Version"))
	}
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		setup   func(f *fakeLaunchDarkly)
		wantErr string
	}{
		{name: "a flag that is not found", setup: func(f *fakeLaunchDarkly) { f.missing = true }, wantErr: "was not found in project shop"},
		{name: "a flag that is off already", setup: func(f *fakeLaunchDarkly) { f.on = false }, wantErr: "already off"},
		{name: "a flag that is archived", setup: func(f *fakeLaunchDarkly) { f.archived = true }, wantErr: "archived"},
		{name: "a flag with no configuration in the environment", setup: func(f *fakeLaunchDarkly) { f.noConfig = true }, wantErr: "no configuration"},
		{name: "approvals for every flag", setup: func(f *fakeLaunchDarkly) { f.approvalsRequired = true }, wantErr: "requires approval"},
		{
			name:    "approvals for a tag the flag has",
			setup:   func(f *fakeLaunchDarkly) { f.approvalsRequired, f.approvalTags = true, []string{"other", "checkout"} },
			wantErr: "requires approval",
		},
		{name: "a scheduled change", setup: func(f *fakeLaunchDarkly) { f.scheduled = 2 }, wantErr: "2 scheduled change(s)"},
		{name: "an active workflow", setup: func(f *fakeLaunchDarkly) { f.workflows = 1 }, wantErr: "active workflow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			f.setup(tt.setup)

			_, err := buildAction(t, api, limitedConfig(api)).(action.Snapshotter).Snapshot(context.Background(), json.RawMessage(paramsJSON))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSnapshotLetsTheProposalThroughWhenACheckCannotBeMade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(f *fakeLaunchDarkly)
	}{
		{name: "approvals for a tag the flag does not have", setup: func(f *fakeLaunchDarkly) { f.approvalsRequired, f.approvalTags = true, []string{"other"} }},
		{name: "the environment read is refused", setup: func(f *fakeLaunchDarkly) { f.envStatus = http.StatusForbidden }},
		{name: "the scheduled changes read is refused", setup: func(f *fakeLaunchDarkly) { f.scheduledStatus = http.StatusForbidden }},
		{name: "the workflows read fails", setup: func(f *fakeLaunchDarkly) { f.workflowsStatus = http.StatusInternalServerError }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			f.setup(tt.setup)

			snapshotOf(t, buildAction(t, api, limitedConfig(api)))
		})
	}
}

func TestSnapshotOutsideTheLimitsSendsNothing(t *testing.T) {
	t.Parallel()

	api, _ := newFake(t)

	_, err := buildAction(t, api, limitedConfig(api)).(action.Snapshotter).Snapshot(context.Background(),
		json.RawMessage(`{"project":"other","environment":"production","flag":"checkout-new","change":"turn_off"}`))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, api.Requests())
}

func TestSnapshotReadFailureIsNotARefusal(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	f.setup(func(f *fakeLaunchDarkly) { f.flagStatus = http.StatusInternalServerError })

	_, err := buildAction(t, api, limitedConfig(api)).(action.Snapshotter).Snapshot(context.Background(), json.RawMessage(paramsJSON))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrLimit)
	require.NotErrorIs(t, err, action.ErrPermanent)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	snap := snapshotOf(t, a)

	res, err := a.Execute(context.Background(), invocation(snap))
	require.NoError(t, err)
	require.Equal(t, "Turned off flag checkout-new in shop/production.", res.Summary)
	require.Equal(t, []action.Link{{Title: "Flag checkout-new", URL: api.URL() + "/shop/production/features/checkout-new"}}, res.Links)

	on, _ := f.state()
	require.False(t, on)

	patches := f.patchLog()
	require.Len(t, patches, 1)
	require.Equal(t, semanticPatchType, patches[0].contentType)
	require.JSONEq(t, turnOffPatch, patches[0].body)
}

func TestExecuteRetryChangesOnce(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	inv := invocation(snapshotOf(t, a))

	for range 2 {
		res, err := a.Execute(context.Background(), inv)
		require.NoError(t, err)
		require.Equal(t, "Turned off flag checkout-new in shop/production.", res.Summary)
	}

	require.Len(t, f.patchLog(), 1)
}

func TestExecuteWhenSomeoneTurnedItOffAlready(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	inv := invocation(snapshotOf(t, a))

	f.setup(func(f *fakeLaunchDarkly) { f.on, f.version = false, f.version+1 })

	_, err := a.Execute(context.Background(), inv)
	require.NoError(t, err)
	require.Empty(t, f.patchLog())
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(f *fakeLaunchDarkly)
	}{
		{name: "the environment changed after the snapshot", setup: func(f *fakeLaunchDarkly) { f.version++ }},
		{name: "the flag was archived after the snapshot", setup: func(f *fakeLaunchDarkly) { f.archived = true }},
		{name: "the environment is gone", setup: func(f *fakeLaunchDarkly) { f.noConfig = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			a := buildAction(t, api, limitedConfig(api))
			inv := invocation(snapshotOf(t, a))

			f.setup(tt.setup)

			_, err := a.Execute(context.Background(), inv)
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.patchLog())
		})
	}
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	snap := action.Snapshot{
		Target: "launchdarkly:proj/shop/env/production/flag/checkout-new",
		Before: json.RawMessage(`{"on":true}`), After: json.RawMessage(`{"on":false}`), Version: "7",
	}

	tests := []struct {
		name    string
		inv     func() action.Invocation
		limits  func(c *Config)
		wantErr error
	}{
		{name: "no snapshot", inv: func() action.Invocation { i := invocation(snap); i.Snapshot = nil; return i }, wantErr: action.ErrPermanent},
		{name: "a snapshot of another flag", inv: func() action.Invocation {
			s := snap
			s.Target = "launchdarkly:proj/shop/env/production/flag/checkout-old"

			return invocation(s)
		}, wantErr: action.ErrPermanent},
		{name: "a snapshot that turns the flag on", inv: func() action.Invocation {
			s := snap
			s.Before, s.After = s.After, s.Before

			return invocation(s)
		}, wantErr: action.ErrPermanent},
		{name: "a snapshot that does not decode", inv: func() action.Invocation {
			s := snap
			s.After = json.RawMessage(`[]`)

			return invocation(s)
		}, wantErr: action.ErrPermanent},
		{
			name:    "limits tightened after the approval",
			inv:     func() action.Invocation { return invocation(snap) },
			limits:  func(c *Config) { c.Limits.Projects[0].Flags = []string{"search-*"} },
			wantErr: action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			cfg := limitedConfig(api)

			if tt.limits != nil {
				tt.limits(&cfg)
			}

			_, err := buildAction(t, api, cfg).Execute(context.Background(), tt.inv())
			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, f.patchLog())
			require.Empty(t, api.Requests(), "nothing is sent before the checks pass")
		})
	}
}

func TestExecuteAnswersOfLaunchDarkly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		wantErr       string
		wantPermanent bool
		wantConflict  bool
	}{
		{name: "approvals required", status: http.StatusMethodNotAllowed, wantErr: "requires approvals", wantPermanent: true},
		{name: "a pending change would fail", status: http.StatusBadRequest, wantErr: "scheduled change or an approval request", wantPermanent: true},
		{name: "the token is refused", status: http.StatusForbidden, wantErr: "403", wantPermanent: true},
		{name: "a status conflict", status: http.StatusConflict, wantErr: "409", wantConflict: true},
		{name: "rate limited", status: http.StatusTooManyRequests, wantErr: "429"},
		{name: "a server error", status: http.StatusBadGateway, wantErr: "502"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			a := buildAction(t, api, limitedConfig(api))
			inv := invocation(snapshotOf(t, a))

			f.setup(func(f *fakeLaunchDarkly) { f.patchStatus = tt.status })

			_, err := a.Execute(context.Background(), inv)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantConflict, errors.Is(err, action.ErrConflict))
			require.Len(t, f.patchLog(), 1)
		})
	}
}

func TestExecuteFlagGoneIsPermanent(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	inv := invocation(snapshotOf(t, a))

	f.setup(func(f *fakeLaunchDarkly) { f.missing = true })

	_, err := a.Execute(context.Background(), inv)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestUndo(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	snap := snapshotOf(t, a)

	_, err := a.Execute(context.Background(), invocation(snap))
	require.NoError(t, err)

	undo := invocation(snap)
	undo.ProposalID, undo.UndoOf = "p2", "p1"

	res, err := a.(action.Undoer).Undo(context.Background(), undo, snap, false)
	require.NoError(t, err)
	require.Equal(t, "Turned on flag checkout-new in shop/production.", res.Summary)

	on, _ := f.state()
	require.True(t, on)

	patches := f.patchLog()
	require.Len(t, patches, 2)
	require.JSONEq(t, turnOnPatch, patches[1].body)
}

func TestUndoRepeatsSafely(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		force bool
	}{
		{name: "without force", force: false},
		{name: "with force", force: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFake(t)
			a := buildAction(t, api, limitedConfig(api))
			snap := snapshotOf(t, a)

			_, err := a.Execute(context.Background(), invocation(snap))
			require.NoError(t, err)

			undo := invocation(snap)
			undo.UndoOf = "p1"

			for range 2 {
				_, err := a.(action.Undoer).Undo(context.Background(), undo, snap, tt.force)
				require.NoError(t, err)
			}

			require.Len(t, f.patchLog(), 2, "one turn off and one turn on")
		})
	}
}

func TestUndoKeepsOtherChanges(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	snap := snapshotOf(t, a)

	_, err := a.Execute(context.Background(), invocation(snap))
	require.NoError(t, err)

	f.setup(func(f *fakeLaunchDarkly) { f.version += 5 })

	_, err = a.(action.Undoer).Undo(context.Background(), invocation(snap), snap, false)
	require.NoError(t, err, "a later edit to another field is no conflict")

	on, _ := f.state()
	require.True(t, on)
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	api, f := newFake(t)
	a := buildAction(t, api, limitedConfig(api))
	snap := snapshotOf(t, a)

	_, err := a.Execute(context.Background(), invocation(snap))
	require.NoError(t, err)

	other := snap
	other.Target = "launchdarkly:proj/shop/env/production/flag/checkout-old"

	_, err = a.(action.Undoer).Undo(context.Background(), invocation(snap), other, false)
	require.ErrorIs(t, err, action.ErrPermanent)

	f.setup(func(f *fakeLaunchDarkly) { f.patchStatus = http.StatusMethodNotAllowed })

	_, err = a.(action.Undoer).Undo(context.Background(), invocation(snap), snap, false)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "requires approvals")
}

// emptyToken resolves every secret to blanks.
type emptyToken struct{}

func (emptyToken) Resolve(_ context.Context, _ registry.SecretRef) ([]byte, error) {
	return []byte(" \n"), nil
}

func TestEmptyTokenIsAnError(t *testing.T) {
	t.Parallel()

	api, _ := newFake(t)

	a, err := New(context.Background(), "flag-off", limitedConfig(api), registry.Deps{Secrets: emptyToken{}, HTTP: api.Client()})
	require.NoError(t, err)

	_, err = a.(action.Snapshotter).Snapshot(context.Background(), json.RawMessage(paramsJSON))
	require.ErrorIs(t, err, errEmptyToken)
	require.Empty(t, api.Requests())
}

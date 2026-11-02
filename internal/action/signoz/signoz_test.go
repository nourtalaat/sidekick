package signoz

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// limitsOf are the limits the tests use unless a test changes them.
func limitsOf() Limits {
	return Limits{
		RuleIDs: []string{ruleOne},
		Fields: []string{
			"alert", "description", "disabled", "labels", "annotations", "preferredChannels",
			"evalWindow", "frequency", "condition", "evaluation", "notificationSettings",
		},
	}
}

// newTestAction returns the action, talking to api, with the limits given.
func newTestAction(t *testing.T, api *fakehttp.Server, limits Limits) *AlertRule {
	t.Helper()

	a, err := New(t.Context(), "signoz-alert-rule", Config{URL: api.URL(), APIKeySecret: keyRef, Limits: limits},
		registry.Deps{Secrets: fakeKeys{}, HTTP: api.Client(), Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	rule, ok := a.(*AlertRule)
	require.True(t, ok)

	return rule
}

// invocationOf is the approved request to run params with snap.
func invocationOf(params string, snap *action.Snapshot) action.Invocation {
	return action.Invocation{
		ProposalID:      "0192d3a4-0000-7000-8000-0000000000f1",
		InvestigationID: "0192d3a4-0000-7000-8000-0000000000e2",
		Params:          json.RawMessage(params),
		IdempotencyKey:  "sidekick/0192d3a4-0000-7000-8000-0000000000f1",
		ApprovedBy:      "u1",
		Snapshot:        snap,
	}
}

// paramsOf are the parameters that set the fields in set on the rule.
func paramsOf(set string) string {
	return `{"ruleId": "` + ruleOne + `", "set": ` + set + `}`
}

// raiseTarget is the parameters that raise the threshold of v1Rule to 10.
var raiseTarget = paramsOf(`{"condition.target": 10}`)

// proposed makes the snapshot of params.
func proposed(t *testing.T, a *AlertRule, params string) action.Snapshot {
	t.Helper()

	snap, err := a.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	return snap
}

// requireHoldsAfter requires the rule to have the values the snapshot writes,
// as SigNoz reads them back.
func requireHoldsAfter(t *testing.T, sz *fakeSigNoz, snap action.Snapshot) {
	t.Helper()

	ok, err := holdsAfter(sz.definitionNow(t), snap)
	require.NoError(t, err)
	require.True(t, ok, "the rule has the new values")
}

// requireHoldsBefore requires the rule to have the values from before the
// snapshot's change.
func requireHoldsBefore(t *testing.T, sz *fakeSigNoz, snap action.Snapshot) {
	t.Helper()

	ok, err := holdsBefore(sz.definitionNow(t), snap)
	require.NoError(t, err)
	require.True(t, ok, "the rule has the old values")
}

// requireOnlyTheRuleAPI requires that the action asked SigNoz for nothing but
// the version and the rule, with its key, and never for the test endpoint,
// which sends a real notification.
func requireOnlyTheRuleAPI(t *testing.T, api *fakehttp.Server) {
	t.Helper()

	for _, req := range api.Requests() {
		require.True(t, req.Path == "/api/v1/version" || req.Path == "/api/v2/rules/"+ruleOne, "request to %s", req.Path)
		require.Equal(t, "fake-apiKey", req.Header.Get("SIGNOZ-API-KEY"))
	}
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())

	snap := proposed(t, a, paramsOf(`{"condition.target": 10, "labels.team": "search", "labels.severity": null}`))

	require.Equal(t, "signoz:rule/"+ruleOne, snap.Target)
	require.Equal(t, sz.updatedNow(), snap.Version)
	require.JSONEq(t, v1Rule, string(snap.Before), "the rule as SigNoz reads it, without the keys it sets")

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{
		{Path: "condition.target", Before: new("5"), After: new("10")},
		{Path: "labels.severity", Before: new(`"critical"`)},
		{Path: "labels.team", Before: new(`"payments"`), After: new(`"search"`)},
	}, changes)

	require.Zero(t, sz.putCount(), "proposing writes nothing")
	requireOnlyTheRuleAPI(t, api)
}

func TestSnapshotOfAV2Rule(t *testing.T) {
	t.Parallel()

	api, _ := newFakeSigNoz(t, storedV2)
	a := newTestAction(t, api, limitsOf())

	snap := proposed(t, a, paramsOf(`{
      "condition.thresholds.spec[0].target": 1000, "condition.thresholds.spec[1].recoveryTarget": 500,
      "evaluation.spec.evalWindow": "15m", "notificationSettings.renotify.interval": "1h"}`))

	require.JSONEq(t, v2Rule, string(snap.Before))

	changes, err := action.Changes(snap)
	require.NoError(t, err)
	require.Equal(t, []action.Change{
		{Path: "condition.thresholds.spec[0].target", Before: new("900"), After: new("1000")},
		{Path: "condition.thresholds.spec[1].recoveryTarget", Before: new("null"), After: new("500")},
		{Path: "evaluation.spec.evalWindow", Before: new(`"5m"`), After: new(`"15m"`)},
		{Path: "notificationSettings.renotify.interval", Before: new(`"4h"`), After: new(`"1h"`)},
	}, changes)
}

func TestSnapshotOfADelete(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	limits := limitsOf()
	limits.AllowDelete = true
	a := newTestAction(t, api, limits)

	snap := proposed(t, a, `{"ruleId": "`+ruleOne+`", "delete": true}`)
	require.True(t, snap.Deletes())
	require.JSONEq(t, v1Rule, string(snap.Before))
	require.Zero(t, sz.deleteCount())
}

func TestSnapshotAllowsARuleByItsName(t *testing.T) {
	t.Parallel()

	api, _ := newFakeSigNoz(t, storedV1)

	limits := limitsOf()
	limits.RuleIDs, limits.RuleNames = nil, []string{"Checkout *"}
	snap := proposed(t, newTestAction(t, api, limits), raiseTarget)
	require.Equal(t, "signoz:rule/"+ruleOne, snap.Target)

	limits.RuleNames = []string{"checkout *"}

	_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(raiseTarget))
	require.ErrorIs(t, err, action.ErrLimit, "a pattern is case sensitive")
}

func TestAChangeCannotTakeARuleOutOfTheLimits(t *testing.T) {
	t.Parallel()

	byName := limitsOf()
	byName.RuleIDs, byName.RuleNames = nil, []string{"Checkout *"}

	tests := []struct {
		name    string
		limits  Limits
		params  string
		wantErr string
	}{
		{name: "renamed out of the pattern", limits: byName, params: paramsOf(`{"alert": "Payments errors"}`), wantErr: "the change would rename rule " + ruleOne + " out of limits.ruleNames"},
		{name: "renamed to another name the pattern matches", limits: byName, params: paramsOf(`{"alert": "Checkout errors renamed"}`)},
		{name: "a rule allowed by its ID can be renamed", limits: limitsOf(), params: paramsOf(`{"alert": "Payments errors"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeSigNoz(t, storedV1)

			_, err := newTestAction(t, api, tt.limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}

	// A snapshot made while the rule was allowed by its ID is not written once
	// only a pattern allows it.
	api, sz := newFakeSigNoz(t, storedV1)
	params := paramsOf(`{"alert": "Payments errors"}`)
	snap := proposed(t, newTestAction(t, api, limitsOf()), params)

	_, err := newTestAction(t, api, byName).Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Zero(t, sz.putCount())
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		limits    func(l *Limits)
		change    func(sz *fakeSigNoz)
		wantLimit bool
		wantPerm  bool
		wantErr   string
	}{
		{
			name: "a field outside limits.fields", params: raiseTarget, limits: func(l *Limits) { l.Fields = []string{"labels"} },
			wantLimit: true, wantErr: `the field "condition.target" is not one of limits.fields`,
		},
		{
			name: "a field that does not exist", params: paramsOf(`{"condition.targt": 3}`), limits: func(l *Limits) { l.Fields = []string{"condition"} },
			wantLimit: true, wantErr: `"condition.targt"`,
		},
		{
			name: "a label whose name has a dot", params: paramsOf(`{"labels.service.name": "checkout"}`),
			wantLimit: true, wantErr: "a dot in a path separates keys",
		},
		{
			name: "a value of the wrong form", params: paramsOf(`{"condition.target": "ten"}`),
			wantLimit: true, wantErr: "must be a number",
		},
		{
			name: "a change that changes nothing", params: paramsOf(`{"condition.target": 5}`),
			wantLimit: true, wantErr: "would change nothing",
		},
		{
			name: "a rule that is not allowed", params: `{"ruleId": "` + ruleTwo + `", "set": {"condition.target": 10}}`,
			wantLimit: true, wantErr: "ruleId is not one of limits.ruleIds",
		},
		{
			name: "a rule whose name matches no pattern", params: raiseTarget,
			limits:    func(l *Limits) { l.RuleIDs, l.RuleNames = nil, []string{"Payments *"} },
			wantLimit: true, wantErr: "the rule name matches none of limits.ruleNames",
		},
		{name: "a rule ID that is no UUID", params: `{"ruleId": "12345", "set": {"condition.target": 10}}`, wantErr: "ruleId must be the UUID of the rule"},
		{
			name: "a delete when delete is not allowed", params: `{"ruleId": "` + ruleOne + `", "delete": true}`,
			wantLimit: true, wantErr: "limits.allowDelete",
		},
		{name: "set and delete together", params: `{"ruleId": "` + ruleOne + `", "delete": true, "set": {"alert": "x"}}`, wantErr: "give either set or delete"},
		{name: "nothing to change", params: `{"ruleId": "` + ruleOne + `"}`, wantErr: "give set with the fields to change"},
		{
			name: "a rule that does not exist", params: raiseTarget,
			change:   func(sz *fakeSigNoz) { sz.getStatus = http.StatusNotFound },
			wantPerm: true, wantErr: "SigNoz answered 404",
		},
		{
			name: "a key that SigNoz refuses", params: raiseTarget,
			change:   func(sz *fakeSigNoz) { sz.getStatus = http.StatusForbidden },
			wantPerm: true, wantErr: "SigNoz answered 403",
		},
		{
			name: "SigNoz failing", params: raiseTarget,
			change: func(sz *fakeSigNoz) { sz.getStatus = http.StatusServiceUnavailable }, wantErr: "SigNoz answered 503",
		},
		{
			name: "a SigNoz that is too old", params: raiseTarget,
			change:   func(sz *fakeSigNoz) { sz.release = "v0.100.0" },
			wantPerm: true, wantErr: "SigNoz v0.100.0 is older than v0.134.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, sz := newFakeSigNoz(t, storedV1)
			if tt.change != nil {
				sz.configure(tt.change)
			}

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			_, err := newTestAction(t, api, limits).Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantLimit, isLimit(err))
			require.Equal(t, tt.wantPerm, isPermanent(err))
			require.Zero(t, sz.putCount())
		})
	}
}

func isLimit(err error) bool {
	return errors.Is(err, action.ErrLimit)
}

func TestSnapshotOfAV2RuleRefusesTheFieldsOfV1(t *testing.T) {
	t.Parallel()

	api, _ := newFakeSigNoz(t, storedV2)
	a := newTestAction(t, api, limitsOf())

	for _, set := range []string{`{"condition.target": 3}`, `{"evalWindow": "10m"}`, `{"preferredChannels": ["email"]}`} {
		_, err := a.Snapshot(t.Context(), json.RawMessage(paramsOf(set)))
		require.ErrorIs(t, err, action.ErrLimit, set)
		require.ErrorContains(t, err, "SigNoz would keep the value and ignore it", set)
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		limits  func(l *Limits)
		wantErr string
		isLimit bool
	}{
		{name: "a change", params: raiseTarget},
		{name: "a delete when it is allowed", params: `{"ruleId": "` + ruleOne + `", "delete": true}`, limits: func(l *Limits) { l.AllowDelete = true }},
		{name: "an ID in capitals", params: `{"ruleId": "0196A1B2-7C3D-7E4F-8A5B-1C2D3E4F5A6B", "set": {"alert": "x"}}`},
		{name: "a rule that a pattern may allow", params: `{"ruleId": "` + ruleTwo + `", "set": {"alert": "x"}}`, limits: func(l *Limits) { l.RuleNames = []string{"x *"} }},
		{name: "a rule that is not allowed", params: `{"ruleId": "` + ruleTwo + `", "set": {"alert": "x"}}`, wantErr: "ruleId is not one of limits.ruleIds", isLimit: true},
		{name: "a field outside limits.fields", params: paramsOf(`{"annotations.a": "b"}`), limits: func(l *Limits) { l.Fields = []string{"labels"} }, wantErr: "is not one of limits.fields", isLimit: true},
		{name: "a delete that is not allowed", params: `{"ruleId": "` + ruleOne + `", "delete": true}`, wantErr: "limits.allowDelete", isLimit: true},
		{name: "a key the action does not take", params: `{"ruleId": "` + ruleOne + `", "set": {"alert": "x"}, "force": true}`, wantErr: "decode the parameters"},
		{name: "a path that is no path", params: paramsOf(`{"labels..x": "b"}`), wantErr: "set:"},
		{name: "not an object", params: `[]`, wantErr: "decode the parameters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			limits := limitsOf()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			err := newTestAction(t, api, limits).Validate(t.Context(), json.RawMessage(tt.params))
			require.Empty(t, api.Requests(), "Validate calls nobody")

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.isLimit, isLimit(err))
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	limits := Limits{RuleIDs: []string{ruleOne}, RuleNames: []string{"checkout *"}, Fields: []string{"condition.target", "labels"}, AllowDelete: true}
	spec := newTestAction(t, api, limits).Spec()

	require.Equal(t, "signoz-alert-rule", spec.Name)
	require.True(t, json.Valid(spec.ParamsSchema))

	for _, want := range []string{
		"Allowed rule IDs: " + ruleOne, "checkout *", "condition.target", "labels.<name>", "A delete cannot be undone in Sidekick",
		"cannot check a new rule without side effects", "shows as disabled",
	} {
		require.Contains(t, spec.Description, want)
	}

	require.NotContains(t, spec.Description, "condition.thresholds.spec[n]", "only the fields the limits allow are listed")

	spec = newTestAction(t, api, Limits{RuleIDs: []string{ruleOne}, Fields: []string{"labels"}}).Spec()
	require.NotContains(t, spec.Description, "A delete cannot be undone")
}

func TestExecute(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	res, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)
	require.Equal(t, `Changed 1 field of rule `+ruleOne+`, "Checkout errors high".`, res.Summary)
	require.Equal(t, []action.Link{{Title: "Alert rule", URL: api.URL() + "/alerts/overview?ruleId=" + ruleOne}}, res.Links)

	requireHoldsAfter(t, sz, snap)

	puts := sz.putBodies()
	require.Len(t, puts, 1)
	require.JSONEq(t, string(snap.After), string(puts[0]), "the rule the approver read is the rule that is written")

	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(puts[0], &sent))

	for _, key := range readOnlyKeys {
		require.NotContains(t, sent, key, "the keys SigNoz sets are not sent")
	}

	require.Contains(t, sent, "disabled", "the whole rule is sent, so a missing disabled cannot enable a paused rule")
	requireOnlyTheRuleAPI(t, api)
}

func TestExecuteSendsTheWholeRuleOfAV2Rule(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV2)
	a := newTestAction(t, api, limitsOf())
	params := paramsOf(`{"condition.thresholds.spec[1].recoveryTarget": 500, "notificationSettings.groupBy": ["service.name", "env"]}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	requireHoldsAfter(t, sz, snap)

	puts := sz.putBodies()
	require.Len(t, puts, 1)
	require.JSONEq(t, string(snap.After), string(puts[0]))
}

func TestExecuteTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)

	res, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err, "a retry after a crash finds the change in place")
	require.Contains(t, res.Summary, "Changed 1 field")
	require.Equal(t, 1, sz.putCount())
}

func TestExecuteAfterSigNozShapedTheWrite(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())

	// SigNoz reads "BELOW" back as "below", and a rule with no labels left
	// back with no labels key at all.
	params := paramsOf(`{"condition.op": "BELOW", "condition.matchType": "AVG", "labels": null, "description": null}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	requireHoldsAfter(t, sz, snap)

	var read map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sz.definitionNow(t), &read))
	require.JSONEq(t, `"below"`, string(mustLookup(t, mustLookup(t, sz.definitionNow(t), "condition"), "op")), "SigNoz reads the operator in lower case")
	require.NotContains(t, read, "labels")
	require.NotContains(t, read, "description")

	_, err = a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err, "a retry finds the change in place, as SigNoz reads it")
	require.Equal(t, 1, sz.putCount())

	_, err = a.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err, "Undo needs no force after SigNoz shaped the write")
	requireHoldsBefore(t, sz, snap)
	require.JSONEq(t, v1Rule, string(sz.definitionNow(t)), "the labels and the description are back")
}

func TestExecuteFindsAnEdit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		edit   func(sz *fakeSigNoz, t *testing.T)
		wantOK bool
	}{
		{name: "another field edited", edit: func(sz *fakeSigNoz, t *testing.T) { sz.someoneEdits(t, map[string]string{"description": `"Edited"`}) }},
		{name: "a save with no change", edit: func(sz *fakeSigNoz, _ *testing.T) { sz.someoneSaves() }},
		{name: "the field changed by hand to the new value", edit: func(sz *fakeSigNoz, t *testing.T) { sz.someoneEdits(t, map[string]string{"condition.target": "10"}) }, wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, sz := newFakeSigNoz(t, storedV1)
			a := newTestAction(t, api, limitsOf())
			snap := proposed(t, a, raiseTarget)

			tt.edit(sz, t)

			_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
			if tt.wantOK {
				require.NoError(t, err)
				require.Zero(t, sz.putCount(), "nothing is left to write")

				return
			}

			require.ErrorIs(t, err, action.ErrConflict)
			require.Zero(t, sz.putCount())
		})
	}
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, nil))
	require.ErrorIs(t, err, action.ErrPermanent, "no snapshot")

	other := snap
	other.Target = "signoz:rule/" + ruleTwo
	_, err = a.Execute(t.Context(), invocationOf(raiseTarget, &other))
	require.ErrorIs(t, err, action.ErrPermanent, "a snapshot of another rule")

	tight := newTestAction(t, api, Limits{RuleIDs: []string{ruleOne}, Fields: []string{"description"}})
	_, err = tight.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrLimit, "limits tightened since the proposal")

	byName := newTestAction(t, api, Limits{RuleNames: []string{"Payments *"}, Fields: limitsOf().Fields})
	_, err = byName.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrLimit, "the rule is no longer one the limits allow")

	require.Zero(t, sz.putCount())

	sz.configure(func(f *fakeSigNoz) { f.release = "v0.133.0" })

	fresh := newTestAction(t, api, limitsOf())
	_, err = fresh.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrPermanent, "SigNoz was downgraded")

	sz.configure(func(f *fakeSigNoz) { f.release, f.getStatus = "v0.148.1", http.StatusInternalServerError })

	_, err = a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "a server error is tried again")
}

func TestExecuteWhenSigNozRejectsTheRule(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())

	// Sidekick cannot know the channels of SigNoz, so it is SigNoz that refuses.
	params := paramsOf(`{"preferredChannels": ["nope"]}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorContains(t, err, "channels: the following channels do not exist: [nope]")
	require.ErrorIs(t, err, action.ErrPermanent, "sending it again cannot help")
	require.Equal(t, 1, sz.putCount())
	requireHoldsBefore(t, sz, snap)
}

func TestExecuteWhenSigNozKeepsAnotherValue(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	sz.configure(func(f *fakeSigNoz) {
		f.mangle = func(rule map[string]any) {
			condition, _ := rule["condition"].(map[string]any)
			condition["target"] = json.Number("99")
		}
	})

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "the rule was written, but SigNoz kept a different value for condition.target")
	require.ErrorContains(t, err, "check the rule at "+api.URL()+"/alerts/overview?ruleId="+ruleOne)
	require.Equal(t, 1, sz.putCount())
}

func TestUndo(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	params := paramsOf(`{"condition.target": 10, "labels.team": "search"}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)

	sz.someoneEdits(t, map[string]string{"description": `"Edited by a person"`})

	res, err := a.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err, "no force is needed while the fields are as the action wrote them")
	require.Equal(t, `Restored 2 fields of rule `+ruleOne+`, "Checkout errors high".`, res.Summary)

	requireHoldsBefore(t, sz, snap)

	var read struct {
		Description string `json:"description"`
	}

	require.NoError(t, json.Unmarshal(sz.definitionNow(t), &read))
	require.Equal(t, "Edited by a person", read.Description, "the changed fields are back, the other edit stays")

	puts := sz.putBodies()
	require.JSONEq(t, string(sz.definitionNow(t)), string(puts[len(puts)-1]), "the whole rule is sent")
}

func TestUndoPutsALabelBack(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	params := paramsOf(`{"labels.severity": null}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.NotContains(t, string(sz.definitionNow(t)), `"severity"`)

	_, err = a.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err)
	require.JSONEq(t, v1Rule, string(sz.definitionNow(t)))
}

func TestUndoAfterSomeoneChangedAFieldTheActionChanged(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)

	sz.someoneEdits(t, map[string]string{"condition.target": "9"})
	writes := sz.putCount()

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, false)

	var conflict *action.ConflictError

	require.ErrorAs(t, err, &conflict)
	require.ErrorIs(t, err, action.ErrConflict)
	require.JSONEq(t, string(sz.definitionNow(t)), string(conflict.Live), "the error carries the rule as it is")
	require.Equal(t, writes, sz.putCount(), "nothing was written")

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, true)
	require.NoError(t, err)
	requireHoldsBefore(t, sz, snap)
}

func TestUndoTwiceHasTheEffectOfOne(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, false)
	require.NoError(t, err)

	writes := sz.putCount()

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, false)
	require.NoError(t, err, "the rule is restored already")
	require.Equal(t, writes, sz.putCount())
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)

	tight := newTestAction(t, api, Limits{RuleIDs: []string{ruleOne}, Fields: []string{"description"}})
	_, err = tight.Undo(t.Context(), invocationOf(paramsOf(`{"description": "x"}`), &snap), snap, false)
	require.ErrorIs(t, err, action.ErrLimit, "limits tightened since the change")

	gone := action.Snapshot{Target: snap.Target, Before: snap.Before}
	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &gone), gone, false)
	require.ErrorIs(t, err, action.ErrPermanent, "a delete cannot be undone")

	sz.configure(func(f *fakeSigNoz) { f.getStatus = http.StatusInternalServerError })

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, false)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "a server error is tried again")
}

func TestDelete(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	limits := limitsOf()
	limits.AllowDelete = true
	a := newTestAction(t, api, limits)

	params := `{"ruleId": "` + ruleOne + `", "delete": true}`
	snap := proposed(t, a, params)

	res, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)
	require.Equal(t, `Deleted rule `+ruleOne+`, "Checkout errors high".`, res.Summary)
	require.Equal(t, 1, sz.deleteCount())

	again, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err, "a rule that is gone already counts as deleted")
	require.Equal(t, "Rule "+ruleOne+" is deleted.", again.Summary)
	require.Equal(t, 1, sz.deleteCount())

	_, err = a.Undo(t.Context(), invocationOf(params, &snap), snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestDeleteNotAfterAnEdit(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	limits := limitsOf()
	limits.AllowDelete = true
	a := newTestAction(t, api, limits)

	params := `{"ruleId": "` + ruleOne + `", "delete": true}`
	snap := proposed(t, a, params)

	sz.someoneEdits(t, map[string]string{"description": `"Edited by a person"`})

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.Zero(t, sz.deleteCount())

	tight := newTestAction(t, api, limitsOf())
	_, err = tight.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorIs(t, err, action.ErrLimit, "delete was allowed when it was proposed, not now")
}

func TestDeleteOfARuleAPlannedMaintenanceUses(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	sz.configure(func(f *fakeSigNoz) { f.inUse = true })

	limits := limitsOf()
	limits.AllowDelete = true
	a := newTestAction(t, api, limits)

	params := `{"ruleId": "` + ruleOne + `", "delete": true}`
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.ErrorContains(t, err, "referenced by a planned maintenance")
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestAViewerKeyCannotWrite(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	sz.configure(func(f *fakeSigNoz) { f.viewerOnly = true })

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorContains(t, err, "SigNoz answered 403: only editors can do this")
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestTheHeaderOfTheConfigIsUsed(t *testing.T) {
	t.Parallel()

	api, _ := newFakeSigNoz(t, storedV1)

	a, err := New(t.Context(), "x", Config{URL: api.URL(), APIKeySecret: keyRef, APIKeyHeader: "X-Key", Limits: limitsOf()},
		registry.Deps{Secrets: fakeKeys{}, HTTP: api.Client()})
	require.NoError(t, err)

	_, err = a.(*AlertRule).Snapshot(t.Context(), json.RawMessage(raiseTarget))
	require.NoError(t, err)

	for _, req := range api.Requests() {
		require.Equal(t, "fake-apiKey", req.Header.Get("X-Key"))
		require.Empty(t, req.Header.Get("SIGNOZ-API-KEY"))
	}
}

func TestADisabledRuleIsNotWrittenBackByAccident(t *testing.T) {
	t.Parallel()

	const notLoaded, disabledByAPerson = "not loaded", "disabled by a person"

	tests := []struct {
		name    string
		reads   string
		params  string
		wantErr string
	}{
		{name: "a threshold of a rule that is not loaded", reads: notLoaded, params: raiseTarget, wantErr: "shows this rule as disabled"},
		{name: "a threshold of a rule a person disabled", reads: disabledByAPerson, params: raiseTarget, wantErr: "shows this rule as disabled"},
		{name: "disabled set to true on a rule that reads disabled", reads: notLoaded, params: paramsOf(`{"disabled": true}`), wantErr: "shows this rule as disabled"},
		{name: "a threshold with disabled set to false", reads: notLoaded, params: paramsOf(`{"condition.target": 10, "disabled": false}`)},
		{name: "disabled set to false alone", reads: disabledByAPerson, params: paramsOf(`{"disabled": false}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stored := storedV1
			if tt.reads == disabledByAPerson {
				stored = strings.Replace(storedV1, `"disabled": false`, `"disabled": true`, 1)
			}

			api, sz := newFakeSigNoz(t, stored)
			sz.configure(func(f *fakeSigNoz) { f.notLoaded = tt.reads == notLoaded })
			a := newTestAction(t, api, limitsOf())

			snap, err := a.Snapshot(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)

			changes, err := action.Changes(snap)
			require.NoError(t, err)
			require.Contains(t, changes, action.Change{Path: "disabled", Before: new("true"), After: new("false")}, "the approver sees the switch")

			_, err = a.Execute(t.Context(), invocationOf(tt.params, &snap))
			require.NoError(t, err)
			requireHoldsAfter(t, sz, snap)
		})
	}
}

func TestADisabledRuleCanBeDeletedAndAnEnabledOneDisabled(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	sz.configure(func(f *fakeSigNoz) { f.notLoaded = true })

	limits := limitsOf()
	limits.AllowDelete = true
	a := newTestAction(t, api, limits)

	snap := proposed(t, a, `{"ruleId": "`+ruleOne+`", "delete": true}`)
	require.True(t, snap.Deletes())

	api, _ = newFakeSigNoz(t, storedV1)
	a = newTestAction(t, api, limitsOf())
	snap = proposed(t, a, paramsOf(`{"disabled": true}`))
	require.JSONEq(t, `true`, string(mustLookup(t, snap.After, "disabled")))
}

func mustLookup(t *testing.T, doc json.RawMessage, key string) json.RawMessage {
	t.Helper()

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(doc, &fields))
	require.Contains(t, fields, key)

	return fields[key]
}

func TestExecuteRefusesToWriteBackADisabledRule(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	sz.configure(func(f *fakeSigNoz) { f.notLoaded = true })
	a := newTestAction(t, api, limitsOf())

	// A snapshot of a rule that reads as disabled, with a change that does not
	// set disabled, as one made before this check, or by hand, would be.
	before := sz.definitionNow(t)
	after, err := action.Apply(before, []action.Edit{{Path: action.Path{"condition", "target"}, Value: json.RawMessage("10")}})
	require.NoError(t, err)

	snap := action.Snapshot{Target: "signoz:rule/" + ruleOne, Before: before, After: after, Version: sz.updatedNow()}

	_, err = a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "shows this rule as disabled")
	require.Zero(t, sz.putCount())
}

func TestExecuteWhenTheRuleStoppedLoadingAfterTheProposal(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	// SigNoz restarts, and the rule reads as disabled until it is loaded.
	sz.configure(func(f *fakeSigNoz) { f.notLoaded = true })

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.Zero(t, sz.putCount(), "the rule is not written back as disabled")
}

func TestUndoRefusesToWriteBackADisabledRule(t *testing.T) {
	t.Parallel()

	api, sz := newFakeSigNoz(t, storedV1)
	a := newTestAction(t, api, limitsOf())
	snap := proposed(t, a, raiseTarget)

	_, err := a.Execute(t.Context(), invocationOf(raiseTarget, &snap))
	require.NoError(t, err)

	sz.configure(func(f *fakeSigNoz) { f.notLoaded = true })
	writes := sz.putCount()

	_, err = a.Undo(t.Context(), invocationOf(raiseTarget, &snap), snap, true)
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "shows this rule as disabled")
	require.Equal(t, writes, sz.putCount())
}

func TestNewNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), "x", Config{}, registry.Deps{})
	require.Error(t, err)
}

func TestPluginIsRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), Type)
}

func TestEveryRequestAsksForTheRuleOnly(t *testing.T) {
	t.Parallel()

	api, _ := newFakeSigNoz(t, storedV2)
	a := newTestAction(t, api, limitsOf())
	params := paramsOf(`{"condition.thresholds.spec[0].target": 1000}`)
	snap := proposed(t, a, params)

	_, err := a.Execute(t.Context(), invocationOf(params, &snap))
	require.NoError(t, err)

	_, err = a.Undo(t.Context(), invocationOf(params, &snap), snap, false)
	require.NoError(t, err)

	requireOnlyTheRuleAPI(t, api)

	methods := make([]string, 0, len(api.Requests()))
	for _, req := range api.Requests() {
		methods = append(methods, req.Method+" "+req.Path)
	}

	require.False(t, slices.ContainsFunc(methods, func(m string) bool { return strings.HasSuffix(m, "/test") }), "the test endpoint sends a real notification")
}

package prometheusrule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// env is an action wired to a fake API server and a fake validator.
type env struct {
	rule      *Rule
	api       *fakeAPI
	apiServer *fakehttp.Server
	validator *fakeValidator
}

// newEnv builds the action with a config that allows every editable field of
// the group checkout.rules; change adjusts the config.
func newEnv(t *testing.T, change ...func(*Config)) *env {
	t.Helper()

	cfg := Config{Limits: Limits{
		Namespaces: []string{testNamespace}, Names: []string{testName}, Groups: []string{"checkout.rules"},
		Fields: []string{"expr", "for", "keep_firing_for", "labels", "annotations"},
	}}

	for _, c := range change {
		c(&cfg)
	}

	require.NoError(t, cfg.Validate())

	apiServer, api := newFakeAPI(t)
	validatorServer, validator := newFakeValidator(t)

	rule, err := newRule("prom-rule", cfg, testCluster(apiServer), validateclient.New(validatorServer.URL(), validatorServer.Client()))
	require.NoError(t, err)

	return &env{rule: rule, api: api, apiServer: apiServer, validator: validator}
}

// ruleParams makes the parameters that change the given rules.
func ruleParams(rules ...string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"namespace": %q, "name": %q, "rules": [%s]}`, testNamespace, testName, strings.Join(rules, ",")))
}

// latency changes the alert HighLatency.
func latency(set string) string {
	return `{"group": "checkout.rules", "alert": "HighLatency", "set": ` + set + `}`
}

// slower is the usual change: a longer for and a higher severity.
var slower = ruleParams(latency(`{"for": "10m", "labels.severity": "critical"}`))

// invocation makes the invocation of an approved proposal.
func invocation(params json.RawMessage, snap *action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: "p1", Params: params, IdempotencyKey: action.IdempotencyKey("p1"), ApprovedBy: "user-1", Snapshot: snap}
}

// proposed runs the steps of a proposal that need the cluster and returns
// the snapshot.
func (e *env) proposed(t *testing.T, params json.RawMessage) action.Snapshot {
	t.Helper()

	require.NoError(t, e.rule.CheckProposal(context.Background(), params))

	snap, err := e.rule.Snapshot(context.Background(), params)
	require.NoError(t, err)

	return snap
}

func TestSpec(t *testing.T) {
	t.Parallel()

	spec := newEnv(t).rule.Spec()

	require.Equal(t, "prom-rule", spec.Name)
	require.True(t, json.Valid(spec.ParamsSchema))
	require.Contains(t, spec.Description, "Allowed namespaces: monitoring")
	require.Contains(t, spec.Description, "Allowed PrometheusRule names: checkout-alerts")
	require.Contains(t, spec.Description, "Allowed rule groups: checkout.rules")
	require.Contains(t, spec.Description, "Fields that may change, with every field below them: expr, for, keep_firing_for, labels, annotations")
}

func TestValidate(t *testing.T) {
	t.Parallel()

	rule := func(group, kind, name, set string) string {
		return fmt.Sprintf(`{"group": %q, %q: %q, "set": %s}`, group, kind, name, set)
	}

	tests := []struct {
		name      string
		change    []func(*Config)
		params    string
		wantErr   string
		wantLimit bool
	}{
		{name: "an alert", params: string(slower)},
		{name: "a recording rule", params: string(ruleParams(rule("checkout.rules", "record", "job:latency:p99", `{"expr": "sum(x)"}`)))},
		{name: "a removed label", params: string(ruleParams(latency(`{"labels.team": null}`)))},
		{name: "a namespace outside the limits", params: `{"namespace": "other", "name": "checkout-alerts", "rules": []}`, wantErr: "limits.namespaces", wantLimit: true},
		{name: "a name outside the limits", params: `{"namespace": "monitoring", "name": "other", "rules": []}`, wantErr: "limits.names", wantLimit: true},
		{name: "no rules", params: `{"namespace": "monitoring", "name": "checkout-alerts", "rules": []}`, wantErr: "give at least one rule"},
		{name: "a group outside the limits", params: string(ruleParams(rule("checkout.other", "alert", "Other", `{"for": "1m"}`))), wantErr: "limits.groups", wantLimit: true},
		{
			name: "a field outside the limits", change: []func(*Config){func(c *Config) { c.Limits.Fields = []string{"expr"} }},
			params: string(slower), wantErr: `the field "for" is not one of limits.fields`, wantLimit: true,
		},
		{
			name: "a label outside a narrow limit", change: []func(*Config){func(c *Config) { c.Limits.Fields = []string{"labels.severity"} }},
			params: string(ruleParams(latency(`{"labels.team": "x"}`))), wantErr: `"labels.team" is not one of limits.fields`, wantLimit: true,
		},
		{name: "a field that cannot change", params: string(ruleParams(latency(`{"alert": "Renamed"}`))), wantErr: `"alert" is not one of limits.fields`, wantLimit: true},
		{name: "a path that is not one", params: string(ruleParams(latency(`{"labels..x": "y"}`))), wantErr: "invalid field path"},
		{
			name:    "an alert and a record",
			params:  string(ruleParams(`{"group": "checkout.rules", "alert": "A", "record": "B", "set": {"for": "1m"}}`)),
			wantErr: "either alert or record",
		},
		{name: "neither alert nor record", params: string(ruleParams(`{"group": "checkout.rules", "set": {"for": "1m"}}`)), wantErr: "give alert or record"},
		{name: "an empty set", params: string(ruleParams(latency(`{}`))), wantErr: "give set with the fields"},
		{name: "a rule twice", params: string(ruleParams(latency(`{"for": "1m"}`), latency(`{"for": "2m"}`))), wantErr: "is listed twice"},
		{name: "a rule name that starts with a bracket", params: string(ruleParams(rule("checkout.rules", "alert", "[x]", `{"for": "1m"}`))), wantErr: "must not start with a bracket"},
		{name: "an unknown key", params: `{"namespace": "monitoring", "name": "checkout-alerts", "rules": [], "force": true}`, wantErr: "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := newEnv(t, tt.change...).rule.Validate(context.Background(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantLimit {
				require.ErrorIs(t, err, action.ErrLimit)
			}
		})
	}
}

func TestValidateRefusesTooManyFields(t *testing.T) {
	t.Parallel()

	set := map[string]string{}
	for i := range action.MaxChanges + 1 {
		set[fmt.Sprintf("labels.l%d", i)] = "x"
	}

	raw, err := json.Marshal(set)
	require.NoError(t, err)

	err = newEnv(t).rule.Validate(context.Background(), ruleParams(latency(string(raw))))
	require.ErrorContains(t, err, "more than 100 fields")
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	snap, err := e.rule.Snapshot(context.Background(), slower)
	require.NoError(t, err)

	require.Equal(t, "kubernetes:prometheusrule/monitoring/checkout-alerts", snap.Target)
	require.Equal(t, "100", snap.Version)
	require.JSONEq(t, `{"checkout.rules": {"HighLatency": {
	  "expr": "latency > 2", "for": "5m",
	  "labels": {"severity": "warning", "team": "payments"}, "annotations": {"summary": "Latency is high"}}}}`, string(snap.Before))
	require.JSONEq(t, `{"checkout.rules": {"HighLatency": {
	  "expr": "latency > 2", "for": "10m",
	  "labels": {"severity": "critical", "team": "payments"}, "annotations": {"summary": "Latency is high"}}}}`, string(snap.After))

	changes, err := action.Changes(snap)
	require.NoError(t, err)

	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = c.Path
	}

	require.Equal(t, []string{"checkout.rules.HighLatency.for", "checkout.rules.HighLatency.labels.severity"}, paths)

	require.Empty(t, e.api.order(), "a snapshot writes nothing and asks for no dry run")
	require.Empty(t, e.validator.seen(), "the validator is asked by CheckProposal, not by Snapshot")
}

func TestSnapshotTreatsANumberExpressionAsText(t *testing.T) {
	t.Parallel()

	snap, err := newEnv(t).rule.Snapshot(context.Background(),
		ruleParams(`{"group": "checkout.rules", "alert": "AlwaysOn", "set": {"for": "1m"}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"checkout.rules": {"AlwaysOn": {"expr": "1"}}}`, string(snap.Before))
	require.JSONEq(t, `{"checkout.rules": {"AlwaysOn": {"expr": "1", "for": "1m"}}}`, string(snap.After))
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	manyLabels := map[string]string{}
	for i := range action.MaxChanges + 1 {
		manyLabels[fmt.Sprintf("l%d", i)] = "x"
	}

	tooMany, err := json.Marshal(map[string]any{"labels": manyLabels})
	require.NoError(t, err)

	tests := []struct {
		name    string
		setup   func(t *testing.T, e *env)
		params  json.RawMessage
		wantErr string
	}{
		{
			name:    "a rule that is not there",
			params:  ruleParams(`{"group": "checkout.rules", "alert": "Nope", "set": {"for": "1m"}}`),
			wantErr: `has no alert "Nope" in group "checkout.rules"; the rules in the allowed groups are: alert "HighLatency" in group "checkout.rules"`,
		},
		{
			name:    "an alert named like a recording rule",
			params:  ruleParams(`{"group": "checkout.rules", "alert": "job:latency:p99", "set": {"for": "1m"}}`),
			wantErr: "has no alert",
		},
		{
			name:    "a group that is not there",
			setup:   func(t *testing.T, e *env) { e.api.edit(t, func(g []any) { g[0].(map[string]any)["name"] = "renamed" }) },
			params:  slower,
			wantErr: `has no alert "HighLatency" in group "checkout.rules"`,
		},
		{
			name: "two rules with the name",
			setup: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) {
					group := g[0].(map[string]any)
					group["rules"] = append(group["rules"].([]any), map[string]any{"alert": "HighLatency", "expr": "other"})
				})
			},
			params:  slower,
			wantErr: "more than one rule has that name",
		},
		{
			name:    "an expression that is a number",
			params:  ruleParams(latency(`{"expr": 5}`)),
			wantErr: "expr must be text",
		},
		{
			name:    "a removed expression",
			params:  ruleParams(latency(`{"expr": null}`)),
			wantErr: "expr must be text",
		},
		{
			name:    "a for that is a number",
			params:  ruleParams(latency(`{"for": 5}`)),
			wantErr: "for must be a duration",
		},
		{
			name:    "an empty keep_firing_for",
			params:  ruleParams(latency(`{"keep_firing_for": ""}`)),
			wantErr: "keep_firing_for must be a duration",
		},
		{
			name:    "labels that are not text",
			params:  ruleParams(latency(`{"labels": {"a": 1}}`)),
			wantErr: "labels.a must be text",
		},
		{
			name:    "a label inside a field that is not an object",
			params:  ruleParams(latency(`{"for.x": "1m"}`)),
			wantErr: "apply set to the rules",
		},
		{
			name:    "a difference too large to review",
			params:  ruleParams(latency(string(tooMany))),
			wantErr: "more than 100 fields, which an approver cannot review; change fewer rules or fields in one proposal, and split the rest into other proposals",
		},
		{
			name:    "a resource that does not exist",
			setup:   func(_ *testing.T, e *env) { e.api.gone = true },
			params:  slower,
			wantErr: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(t, e)
			}

			_, err := e.rule.Snapshot(context.Background(), tt.params)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit, "the model reads the message and may propose again")
			require.Empty(t, e.api.order())
		})
	}
}

func TestSnapshotRefusesAParentOfAnAllowedField(t *testing.T) {
	t.Parallel()

	e := newEnv(t, func(c *Config) { c.Limits.Fields = []string{"for", "labels.severity"} })

	_, err := e.rule.Snapshot(context.Background(), ruleParams(latency(`{"labels": {"severity": "critical"}}`)))
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "limits.fields")
}

func TestCheckProposal(t *testing.T) {
	t.Parallel()

	e := newEnv(t)

	require.NoError(t, e.rule.CheckProposal(context.Background(), slower))

	seen := e.validator.seen()
	require.Len(t, seen, 1)
	require.Equal(t, validateclient.FormatPrometheusRules, seen[0].Format)
	require.Len(t, seen[0].Files, 1)
	require.Equal(t, "rules.yaml", seen[0].Files[0].Path)

	var file struct {
		Groups []struct {
			Name     string `json:"name"`
			Interval string `json:"interval"`
			Rules    []map[string]any
		} `json:"groups"`
	}

	require.NoError(t, json.Unmarshal([]byte(seen[0].Files[0].Content), &file))
	require.Len(t, file.Groups, 1, "only the group of the changed rule")
	require.Equal(t, "checkout.rules", file.Groups[0].Name)
	require.Equal(t, "30s", file.Groups[0].Interval)
	require.Len(t, file.Groups[0].Rules, 4, "the whole group")
	require.Equal(t, "10m", file.Groups[0].Rules[0]["for"], "the changed rule is in the file as changed")
	require.Equal(t, "1", file.Groups[0].Rules[2]["expr"])

	require.Equal(t, []bool{true}, e.api.order(), "one dry run and no write")
	require.Equal(t, "100", e.api.version())

	body := e.api.dryRuns()[0].body
	require.Equal(t, map[string]any{"resourceVersion": "100"}, body["metadata"])
	require.Len(t, body["spec"].(map[string]any)["groups"].([]any), 2, "the whole list is sent, because a merge patch replaces a list whole")
}

func TestCheckProposalRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(e *env)
		params     json.RawMessage
		wantErr    string
		wantDryRun bool
	}{
		{
			name:    "promtool finds an error",
			params:  ruleParams(latency(`{"expr": "BAD_EXPR"}`)),
			wantErr: `line 12: group "checkout.rules", rule 1, "HighLatency": could not parse expression`,
		},
		{
			name:    "the validator is busy",
			setup:   func(e *env) { e.validator.status = http.StatusServiceUnavailable },
			params:  slower,
			wantErr: validateclient.ErrBusy.Error(),
		},
		{
			name:       "the API server refuses the value",
			params:     ruleParams(latency(`{"for": "five minutes"}`)),
			wantErr:    "duration pattern",
			wantDryRun: true,
		},
		{
			name:       "the API server would drop the field",
			setup:      func(e *env) { e.api.oldCRD = true },
			params:     ruleParams(latency(`{"keep_firing_for": "5m"}`)),
			wantErr:    `would store "checkout.rules.HighLatency.keep_firing_for" other than the change says`,
			wantDryRun: true,
		},
		{
			name:    "a rule that is not there",
			params:  ruleParams(`{"group": "checkout.rules", "alert": "Nope", "set": {"for": "1m"}}`),
			wantErr: "has no alert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			if tt.setup != nil {
				tt.setup(e)
			}

			err := e.rule.CheckProposal(context.Background(), tt.params)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, e.api.writes())

			if tt.wantDryRun {
				require.Len(t, e.api.dryRuns(), 1)
			} else {
				require.Empty(t, e.api.dryRuns())
			}
		})
	}
}

func TestCheckProposalErrorsHoldNoToken(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	e.api.tokenStatus = http.StatusForbidden
	e.api.tokenEcho = ownToken

	err := e.rule.CheckProposal(context.Background(), slower)
	require.Error(t, err)
	require.NotContains(t, err.Error(), ownToken)

	_, err = e.rule.Snapshot(context.Background(), slower)
	require.Error(t, err)
	require.NotContains(t, err.Error(), ownToken)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)

	res, err := e.rule.Execute(context.Background(), invocation(slower, &snap))
	require.NoError(t, err)
	require.Equal(t, "Changed 2 fields in 1 rule(s) of PrometheusRule monitoring/checkout-alerts.", res.Summary)

	require.Equal(t, []bool{true, true, false}, e.api.order(), "the dry run of the proposal, the dry run before the write, then the write")
	require.Len(t, e.validator.seen(), 2, "the validator checks again before the write")

	write := e.api.writes()[0]
	require.Equal(t, "application/merge-patch+json", write.contentType)
	require.Equal(t, map[string]any{"resourceVersion": "100"}, write.body["metadata"])
	require.Equal(t, "101", e.api.version())

	stored := e.api.stored(t)
	require.Contains(t, stored, `"for":"10m"`)
	require.Contains(t, stored, `"severity":"critical"`)
	require.Contains(t, stored, `"team":"payments"`, "the other labels stay")
	require.Contains(t, stored, `"interval":"30s"`, "the other fields of the group stay")
	require.Contains(t, stored, `{"alert":"AlwaysOn","expr":1}`, "a rule that was not named stays as it was written")
	require.Contains(t, stored, `"name":"checkout.other"`)

	after, _, err := e.api.groups(t).view([]ruleRef{{group: "checkout.rules", name: "HighLatency"}})
	require.NoError(t, err)
	require.JSONEq(t, `{"checkout.rules": {"HighLatency": `+extract(t, string(snap.After))+`}}`, string(after), "it reads back as the snapshot says")
}

func TestExecuteAgainAfterACrashWritesNothing(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	before := len(e.api.writes())

	res, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err, "a worker that died after the write finds the change in place")
	require.Contains(t, res.Summary, "Changed 2 fields")
	require.Len(t, e.api.writes(), before)
}

func TestExecuteIgnoresOtherChangesToTheResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(t *testing.T, e *env)
		stored string
	}{
		{name: "a status write of the operator", change: func(_ *testing.T, e *env) { e.api.touch() }},
		{
			name: "an edit of another rule",
			change: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) { findRule(g, "checkout.rules", "ErrorRate")["for"] = "30m" })
			},
			stored: `"for":"30m"`,
		},
		{
			name: "an edit of another group",
			change: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) { findRule(g, "checkout.other", "Other")["expr"] = "up == 1" })
			},
			stored: `"expr":"up == 1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			snap := e.proposed(t, slower)
			require.Equal(t, "100", snap.Version)

			tt.change(t, e)

			_, err := e.rule.Execute(context.Background(), invocation(slower, &snap))
			require.NoError(t, err, "the version of the snapshot is not compared")

			write := e.api.writes()[0]
			require.Equal(t, map[string]any{"resourceVersion": "101"}, write.body["metadata"], "the write holds the version that was just read")

			stored := e.api.stored(t)
			require.Contains(t, stored, `"for":"10m"`)

			if tt.stored != "" {
				require.Contains(t, stored, tt.stored, "the other edit is kept")
			}
		})
	}
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		change     func(t *testing.T, e *env)
		wantDryRun bool
	}{
		{
			name: "someone changed a field of the rule",
			change: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) { findRule(g, "checkout.rules", "HighLatency")["for"] = "7m" })
			},
		},
		{
			name: "someone changed a label that the change did not touch",
			change: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) {
					findRule(g, "checkout.rules", "HighLatency")["labels"].(map[string]any)["team"] = "ops"
				})
			},
		},
		{
			name: "someone removed the rule",
			change: func(t *testing.T, e *env) {
				e.api.edit(t, func(g []any) {
					group := g[0].(map[string]any)
					group["rules"] = group["rules"].([]any)[1:]
				})
			},
		},
		{
			name: "someone wrote between the read and the write",
			change: func(_ *testing.T, e *env) {
				e.api.beforeWrite = e.api.touch
			},
			wantDryRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)
			snap := e.proposed(t, slower)

			tt.change(t, e)
			changed := e.api.stored(t)

			_, err := e.rule.Execute(context.Background(), invocation(slower, &snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Equal(t, changed, e.api.stored(t), "nothing was written")

			if tt.wantDryRun {
				require.Equal(t, []bool{true, true, false}, e.api.order(), "the API server refused the write itself")
			} else {
				require.Equal(t, []bool{true}, e.api.order(), "no dry run and no write: only the one of the proposal")
			}
		})
	}
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(e *env)
		params        json.RawMessage
		snapshot      func(snap action.Snapshot) *action.Snapshot
		change        func(c *Config)
		wantErr       string
		wantPermanent bool
		wantLimit     bool
	}{
		{
			name:     "no snapshot",
			params:   slower,
			snapshot: func(action.Snapshot) *action.Snapshot { return nil },
			wantErr:  "the proposal has no snapshot", wantPermanent: true,
		},
		{
			name:   "a snapshot of another resource",
			params: slower,
			snapshot: func(s action.Snapshot) *action.Snapshot {
				s.Target = "kubernetes:prometheusrule/monitoring/other"

				return &s
			},
			wantErr: "the snapshot is of another resource", wantPermanent: true,
		},
		{
			name:   "a snapshot that deletes",
			params: slower,
			snapshot: func(s action.Snapshot) *action.Snapshot {
				s.After = nil

				return &s
			},
			wantErr: "deleting a rule is not supported", wantPermanent: true,
		},
		{
			name:      "limits that changed since",
			params:    slower,
			change:    func(c *Config) { c.Limits.Fields = []string{"expr"} },
			wantErr:   "limits.fields",
			wantLimit: true,
		},
		{
			name:          "promtool finds an error",
			params:        ruleParams(latency(`{"expr": "BAD_EXPR"}`)),
			wantErr:       "the validator found errors",
			wantPermanent: true,
		},
		{
			name:          "the API server refuses the value",
			params:        ruleParams(latency(`{"for": "five minutes"}`)),
			wantErr:       "duration pattern",
			wantPermanent: true,
		},
		{
			name:          "the account may not ask for a token",
			setup:         func(e *env) { e.api.tokenStatus = http.StatusForbidden },
			params:        slower,
			wantErr:       "forbidden",
			wantPermanent: true,
		},
		{
			name:    "the validator is busy",
			setup:   func(e *env) { e.validator.status = http.StatusServiceUnavailable },
			params:  slower,
			wantErr: validateclient.ErrBusy.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t)

			snap, err := e.rule.Snapshot(context.Background(), tt.params)
			require.NoError(t, err)

			inv := invocation(tt.params, &snap)
			if tt.snapshot != nil {
				inv.Snapshot = tt.snapshot(snap)
			}

			if tt.setup != nil {
				tt.setup(e)
			}

			rule := e.rule
			if tt.change != nil {
				rule = newEnvRule(t, e, tt.change)
			}

			_, err = rule.Execute(context.Background(), inv)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			require.Empty(t, e.api.writes())
		})
	}
}

func TestExecuteFailureOfTheWriteIsTriedAgain(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)

	e.api.failWrites = http.StatusInternalServerError

	_, err := e.rule.Execute(context.Background(), invocation(slower, &snap))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrConflict)

	e.api.failWrites = 0

	_, err = e.rule.Execute(context.Background(), invocation(slower, &snap))
	require.NoError(t, err, "the retry writes it")
	require.Contains(t, e.api.stored(t), `"for":"10m"`)
}

func TestEveryCallUsesATokenOfItsOwn(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)

	_, err := e.rule.Execute(context.Background(), invocation(slower, &snap))
	require.NoError(t, err)

	// CheckProposal, Snapshot and Execute.
	require.Equal(t, 3, e.api.mintedCount())

	for _, req := range e.apiServer.Requests() {
		auth := req.Header.Get("Authorization")

		if req.Path == tokenPath {
			require.Equal(t, "Bearer "+ownToken, auth, "only Sidekick's own account asks for tokens")

			continue
		}

		require.True(t, strings.HasPrefix(auth, "Bearer minted-token-"), "the rules are read and written with a minted token: %s %s", req.Method, req.Path)
	}
}

func TestUndo(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	res, err := e.rule.Undo(context.Background(), inv, snap, false)
	require.NoError(t, err, "no force is needed while the rules are as the change left them")
	require.Equal(t, "Restored 2 fields in 1 rule(s) of PrometheusRule monitoring/checkout-alerts.", res.Summary)
	require.JSONEq(t, seedGroups, e.api.stored(t))

	writes := len(e.api.writes())

	res, err = e.rule.Undo(context.Background(), inv, snap, false)
	require.NoError(t, err, "a repeat finds the old values in place")
	require.Contains(t, res.Summary, "Restored 2 fields")
	require.Len(t, e.api.writes(), writes)
}

func TestUndoPutsBackOnlyTheChangedFields(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	e.api.edit(t, func(g []any) {
		findRule(g, "checkout.rules", "HighLatency")["annotations"].(map[string]any)["runbook"] = "https://example.com/rb"
		findRule(g, "checkout.rules", "ErrorRate")["for"] = "1h"
	})

	_, err = e.rule.Undo(context.Background(), inv, snap, false)
	require.NoError(t, err, "fields that the change did not touch may differ")

	stored := e.api.stored(t)
	require.Contains(t, stored, `"for":"5m"`)
	require.Contains(t, stored, `"severity":"warning"`)
	require.Contains(t, stored, `"runbook":"https://example.com/rb"`, "the edit of someone else stays")
	require.Contains(t, stored, `"for":"1h"`, "so does the edit of another rule")
}

func TestUndoNeedsForceAfterAnotherChange(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	e.api.edit(t, func(g []any) { findRule(g, "checkout.rules", "HighLatency")["for"] = "7m" })

	writes := len(e.api.writes())

	_, err = e.rule.Undo(context.Background(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrConflict)

	var conflict *action.ConflictError

	require.ErrorAs(t, err, &conflict)
	require.Contains(t, string(conflict.Live), `"for":"7m"`, "the approver sees what a restore would overwrite")
	require.Len(t, e.api.writes(), writes)

	_, err = e.rule.Undo(context.Background(), inv, snap, true)
	require.NoError(t, err)
	require.JSONEq(t, seedGroups, e.api.stored(t))
}

func TestUndoWhenTheRuleIsGone(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	e.api.edit(t, func(g []any) {
		group := g[0].(map[string]any)
		group["rules"] = group["rules"].([]any)[1:]
	})

	_, err = e.rule.Undo(context.Background(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrConflict)

	_, err = e.rule.Undo(context.Background(), inv, snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "is not there any more")
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	snap := e.proposed(t, slower)
	inv := invocation(slower, &snap)

	deleted := snap
	deleted.After = nil

	_, err := e.rule.Undo(context.Background(), inv, deleted, false)
	require.ErrorIs(t, err, action.ErrPermanent)

	other := snap
	other.Target = "kubernetes:prometheusrule/monitoring/other"

	_, err = e.rule.Undo(context.Background(), inv, other, false)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "another resource")
}

func TestUndoOfANumberExpressionWritesText(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	params := ruleParams(`{"group": "checkout.rules", "alert": "AlwaysOn", "set": {"expr": "up == 1"}}`)
	snap := e.proposed(t, params)
	inv := invocation(params, &snap)

	require.JSONEq(t, `{"checkout.rules": {"AlwaysOn": {"expr": "1"}}}`, string(snap.Before), "the number 1 is read as the text 1")

	_, err := e.rule.Execute(context.Background(), inv)
	require.NoError(t, err)

	_, err = e.rule.Undo(context.Background(), inv, snap, false)
	require.NoError(t, err)

	view, _, err := e.api.groups(t).view([]ruleRef{{group: "checkout.rules", name: "AlwaysOn"}})
	require.NoError(t, err)
	require.JSONEq(t, string(snap.Before), string(view), "it reads back as it did, now written as text, which the CRD allows")
}

func TestARecordingRuleCanChange(t *testing.T) {
	t.Parallel()

	e := newEnv(t)
	params := ruleParams(`{"group": "checkout.rules", "record": "job:latency:p99", "set": {"labels.team": "payments"}}`)
	snap := e.proposed(t, params)

	require.JSONEq(t, `{"checkout.rules": {"job:latency:p99": {"expr": "histogram_quantile(0.99, rate(latency_bucket[5m]))", "labels": {"team": "payments"}}}}`, string(snap.After))

	_, err := e.rule.Execute(context.Background(), invocation(params, &snap))
	require.NoError(t, err)
	require.Contains(t, e.api.stored(t), `"labels":{"team":"payments"}`)

	_, err = e.rule.Undo(context.Background(), invocation(params, &snap), snap, false)
	require.NoError(t, err)
	require.JSONEq(t, seedGroups, e.api.stored(t), "the labels that did not exist are removed")
}

// extract returns the rule with the given name from a definition of one
// group with one rule, as JSON.
func extract(t *testing.T, doc string) string {
	t.Helper()

	var parsed map[string]map[string]json.RawMessage

	require.NoError(t, json.Unmarshal([]byte(doc), &parsed))

	for _, group := range parsed {
		for _, rule := range group {
			return string(rule)
		}
	}

	t.Fatal("no rule in the definition")

	return ""
}

// newEnvRule builds another action for the fake servers of e, with its config
// changed.
func newEnvRule(t *testing.T, e *env, change func(*Config)) *Rule {
	t.Helper()

	cfg := Config{Limits: e.rule.limits}
	change(&cfg)

	rule, err := newRule("prom-rule", cfg, e.rule.cluster, e.rule.validator)
	require.NoError(t, err)

	return rule
}

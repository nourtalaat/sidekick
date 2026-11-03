package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const approvalActions = `
actions:
  rollback:
    type: gated.note
    tokenSecret: { name: rollbacks, key: token }
  rollback-ok:
    type: gated.note
    allowReadWrite: true
    tokenSecret: { name: rollbacks, key: token }
  monitor-note:
    type: note.create
    tokenSecret: { name: notes, key: token }
    limits: { boards: [ops] }
`

// approvalProblem is the problem reported for the action at routes[i].actions[j].
func approvalProblem(i, j int, name, typeName string) string {
	return fmt.Sprintf("routes[%d].actions[%d]: action %q (type %s) is approval-only, so a read-write route may not list it as it is; "+
		"use mode: approved on this route, or set allowReadWrite: true on the action in the config file", i, j, name, typeName)
}

func TestApprovalOnlyActionsOnReadWriteRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		routes string
		want   []string
	}{
		{
			name:   "without the opt-in",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [rollback]}\n  - {name: default}",
			want:   []string{approvalProblem(0, 0, "rollback", "gated.note")},
		},
		{
			name:   "with the opt-in",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [rollback-ok]}\n  - {name: default}",
		},
		{
			name:   "in approved mode",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: approved, actions: [rollback]}\n  - {name: default}",
		},
		{
			name:   "next to an action that is not approval-only",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [monitor-note, rollback-ok, rollback]}\n  - {name: default}",
			want:   []string{approvalProblem(0, 2, "rollback", "gated.note")},
		},
		{
			name:   "an action that is not approval-only",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [monitor-note]}\n  - {name: default}",
		},
		{
			name:   "an action that is not defined is reported once",
			routes: "- {name: ops, match: {intake: [alertmanager]}, mode: read-write, actions: [rollbak]}\n  - {name: default}",
			want:   []string{`routes[0].actions[0]: action "rollbak" is not defined (did you mean "rollback"?)`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yaml := baseYAML + trustOnlyTrustedWebhooks + approvalActions + "\nroutes:\n  " + tt.routes + "\n"

			_, err := Parse(t.Context(), []byte(yaml), testOptions())
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			var errs Errors
			require.ErrorAs(t, err, &errs)
			require.Equal(t, strings.Join(tt.want, "\n"), errs.Error())
		})
	}
}

func TestAllowReadWriteIsAnUnknownKeyForOtherActions(t *testing.T) {
	t.Parallel()

	yaml := baseYAML + trustOnlyTrustedWebhooks + strings.Replace(approvalActions,
		"    type: note.create\n", "    type: note.create\n    allowReadWrite: true\n", 1) + "\nroutes:\n  - {name: default}\n"

	_, err := Parse(t.Context(), []byte(yaml), testOptions())

	var errs Errors
	require.ErrorAs(t, err, &errs)
	require.Contains(t, errs.Error(), "actions.monitor-note.allowReadWrite")
}

func TestCheckRuntimeRouteRefusesApprovalOnlyActions(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(t.Context(), []byte(baseYAML+trustOnlyTrustedWebhooks+approvalActions+"\nroutes:\n  - {name: default}\n"), testOptions())
	require.NoError(t, err)

	tests := []struct {
		name    string
		mode    action.Mode
		actions []string
		want    []string
	}{
		{
			name: "read-write without the opt-in", mode: action.ReadWrite, actions: []string{"rollback"},
			want: []string{
				`routes.checkout.actions[0]: action "rollback" (type gated.note) is approval-only, so a read-write route may not list it as it is; ` +
					"use mode: approved on this route, or set allowReadWrite: true on the action in the config file",
			},
		},
		{name: "read-write with the opt-in", mode: action.ReadWrite, actions: []string{"rollback-ok"}},
		{name: "approved", mode: action.Approved, actions: []string{"rollback"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			route := NewRoute("checkout")
			route.Match = &Match{Intake: []string{"alertmanager"}}
			route.Mode, route.Actions = tt.mode, tt.actions

			requireProblems(t, cfg.CheckRuntimeRoute(t.Context(), route, nil), tt.want)
		})
	}
}

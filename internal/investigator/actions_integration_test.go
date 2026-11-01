//go:build integration

package investigator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// noteAction is an action that writes a note; the tests never run it.
type noteAction struct{}

func (noteAction) Spec() action.Spec {
	return action.Spec{
		Name:         "monitor-note",
		Description:  "Writes a note.",
		ParamsSchema: json.RawMessage(`{"type": "object", "additionalProperties": false, "required": ["title"], "properties": {"title": {"type": "string"}}}`),
	}
}

func (noteAction) Validate(context.Context, json.RawMessage) error { return nil }

func (noteAction) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

// newActions returns a proposal service for the payments route with the
// policy given.
func newActions(t *testing.T, h *harness, payments action.RoutePolicy) *proposals.Service {
	t.Helper()

	svc, err := proposals.New(proposals.Options{
		Pool:    h.pool,
		Actions: map[string]action.Action{"monitor-note": noteAction{}},
		Policy:  action.StaticPolicy{"payments": payments},
	})
	require.NoError(t, err)

	return svc
}

// setMode sets the action mode the test investigation was created with.
func (h *harness) setMode(t *testing.T, mode action.Mode) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET mode = $1 WHERE id = $2", string(mode), testInvestigation)
	require.NoError(t, err)
}

func TestProposeActionIsOfferedOnlyOutsideReadOnlyMode(t *testing.T) {
	t.Parallel()

	approvedRoute := action.RoutePolicy{Mode: action.Approved, Actions: []string{"monitor-note"}}

	tests := []struct {
		name        string
		mode        action.Mode
		route       action.RoutePolicy
		noService   bool
		wantOffered bool
	}{
		{name: "an approved investigation on a route with actions", mode: action.Approved, route: approvedRoute, wantOffered: true},
		{name: "a read-only investigation", mode: action.ReadOnly, route: approvedRoute},
		{name: "a route with no action", mode: action.Approved, route: action.RoutePolicy{Mode: action.Approved}},
		{name: "a route that is read-only now", mode: action.Approved, route: action.RoutePolicy{Mode: action.ReadOnly, Actions: []string{"monitor-note"}}},
		{name: "no actions in the deployment", mode: action.Approved, route: approvedRoute, noService: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{turns: []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport))}})
			h.setMode(t, tt.mode)

			if !tt.noService {
				h.handler.opts.Actions = newActions(t, h, tt.route)
			}

			require.NoError(t, h.run(t))

			tools := promptTools(t, h.steps(t)[0])

			if tt.wantOffered {
				require.Contains(t, tools, proposals.ProposeActionTool)
			} else {
				require.NotContains(t, tools, proposals.ProposeActionTool)
			}
		})
	}
}

func TestProposeActionTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		wantResult    string
		wantProposals int
		wantStatus    string
	}{
		{
			name:          "a proposal inside the limits is stored",
			input:         `{"action": "monitor-note", "params": {"title": "Slow checkout"}, "reason": "p99 doubled after the deploy."}`,
			wantResult:    "Proposed monitor-note as proposal",
			wantProposals: 1,
			wantStatus:    statusOK,
		},
		{
			name:       "parameters that break the schema get the error back",
			input:      `{"action": "monitor-note", "params": {"title": "t", "extra": 1}, "reason": "x"}`,
			wantResult: "params:",
			wantStatus: statusRejected,
		},
		{
			name:       "an action the route does not list",
			input:      `{"action": "other-note", "params": {"title": "t"}, "reason": "x"}`,
			wantResult: "is not allowed",
			wantStatus: statusRejected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{turns: []modeltest.Turn{
				turn(modeltest.Call("p1", proposals.ProposeActionTool, tt.input)),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, inconclusiveReport)),
			}})
			h.setMode(t, action.Approved)
			h.handler.opts.Actions = newActions(t, h, action.RoutePolicy{Mode: action.Approved, Actions: []string{"monitor-note"}})

			require.NoError(t, h.run(t))

			var result db.Step

			for _, s := range h.steps(t) {
				if s.ToolCallID != nil && *s.ToolCallID == "p1" {
					result = s
				}
			}

			parts, err := decodeParts(result.Parts)
			require.NoError(t, err)
			require.Len(t, parts, 1)

			content, ok := parts[0].(model.ToolResult)
			require.True(t, ok)
			require.Contains(t, content.Content, tt.wantResult)
			require.Equal(t, tt.wantStatus, result.Status)

			stored, err := proposals.OfInvestigation(t.Context(), h.pool, testInvestigation)
			require.NoError(t, err)
			require.Len(t, stored, tt.wantProposals)

			if tt.wantProposals == 1 {
				require.Equal(t, action.StatusProposed, stored[0].Status)
				require.Equal(t, "payments", stored[0].Route)

				// The run goes on and ends with its verdict.
				require.Equal(t, StatusFinal, h.investigation(t).Status)
			}
		})
	}
}

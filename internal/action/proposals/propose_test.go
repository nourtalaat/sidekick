package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// offline returns a Service that checks proposals but cannot store them:
// its pool is never used.
func offline(t *testing.T, policy action.Policy, actions ...*fakeNote) *Service {
	t.Helper()

	byName := map[string]action.Action{}
	for _, a := range actions {
		byName[a.name] = a
	}

	return offlineWith(t, policy, byName)
}

// offlineWith is offline for actions of any kind, by name.
func offlineWith(t *testing.T, policy action.Policy, actions map[string]action.Action) *Service {
	t.Helper()

	s, err := New(Options{Pool: new(pgxpool.Pool), Actions: actions, Policy: policy})
	require.NoError(t, err)

	return s
}

func TestSpec(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{
		"payments": {Mode: action.Approved, Actions: []string{"monitor-note", "gone", "other-note"}},
		"quiet":    {Mode: action.ReadOnly, Actions: []string{"monitor-note"}},
		"empty":    {Mode: action.Approved},
	}

	tests := []struct {
		name      string
		route     string
		mode      action.Mode
		want      bool
		wantNames []string
	}{
		{name: "an approved route lists its actions that exist", route: "payments", mode: action.Approved, want: true, wantNames: []string{"monitor-note", "other-note"}},
		{name: "a read-only investigation", route: "payments", mode: action.ReadOnly},
		{name: "an investigation with no mode", route: "payments"},
		{name: "a route that is read-only now", route: "quiet", mode: action.Approved},
		{name: "a route with no actions", route: "empty", mode: action.Approved},
		{name: "a route that does not exist", route: "gone", mode: action.Approved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := offline(t, policy, newFakeNote("monitor-note"), newFakeNote("other-note"))

			spec, ok := s.Spec(tt.route, tt.mode)
			require.Equal(t, tt.want, ok)

			if !tt.want {
				return
			}

			require.Equal(t, ProposeActionTool, spec.Name)

			var schema struct {
				Required   []string `json:"required"`
				Properties struct {
					Action struct {
						Enum []string `json:"enum"`
					} `json:"action"`
				} `json:"properties"`
			}

			require.NoError(t, json.Unmarshal(spec.InputSchema, &schema))
			require.Equal(t, tt.wantNames, schema.Properties.Action.Enum)
			require.Equal(t, []string{"action", "params", "reason"}, schema.Required)

			for _, name := range tt.wantNames {
				require.Contains(t, spec.Description, "- "+name+": Writes a note to a board. Parameters, as a JSON Schema: {")
			}
		})
	}

	var none *Service

	_, ok := none.Spec("payments", action.Approved)
	require.False(t, ok, "a nil Service offers nothing")
}

func TestPrepare(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{
		"payments": {Mode: action.Approved, Actions: []string{"monitor-note"}},
		"quiet":    {Mode: action.ReadOnly, Actions: []string{"monitor-note"}},
	}

	input := func(s string) json.RawMessage { return json.RawMessage(s) }

	tests := []struct {
		name        string
		route       string
		mode        action.Mode
		input       json.RawMessage
		validateErr error
		wantRefusal string
		wantParams  string
		wantReason  string
	}{
		{
			name: "a valid proposal", route: "payments", mode: action.Approved,
			input:      input(`{"action": "monitor-note", "params": {"title": "Slow checkout", "board": "ops"}, "reason": "  Because it is slow.  "}`),
			wantParams: `{"board":"ops","title":"Slow checkout"}`, wantReason: "Because it is slow.",
		},
		{name: "a read-only investigation", route: "payments", mode: action.ReadOnly, input: input(`{}`), wantRefusal: "this investigation is read-only"},
		{name: "input that does not decode", route: "payments", mode: action.Approved, input: input(`{"action": 3}`), wantRefusal: "the input does not decode"},
		{name: "an extra field", route: "payments", mode: action.Approved, input: input(`{"action": "monitor-note", "params": {}, "reason": "x", "mode": "read-write"}`), wantRefusal: "the input does not decode"},
		{
			name: "an action the route does not list", route: "payments", mode: action.Approved,
			input:       input(`{"action": "other-note", "params": {}, "reason": "x"}`),
			wantRefusal: `action other-note is not allowed: route "payments" does not list action "other-note"`,
		},
		{
			name: "a route that is read-only now", route: "quiet", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {}, "reason": "x"}`),
			wantRefusal: `route "quiet" is read-only`,
		},
		{
			name: "a route that is gone", route: "gone", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {}, "reason": "x"}`),
			wantRefusal: `route "gone" is not configured any more`,
		},
		{name: "no params", route: "payments", mode: action.Approved, input: input(`{"action": "monitor-note", "reason": "x"}`), wantRefusal: "params is required"},
		{name: "no reason", route: "payments", mode: action.Approved, input: input(`{"action": "monitor-note", "params": {"board": "ops", "title": "t"}, "reason": " "}`), wantRefusal: "reason is required"},
		{
			name: "a reason that is too long", route: "payments", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {"board": "ops", "title": "t"}, "reason": "` + repeat("x", maxReasonChars+1) + `"}`),
			wantRefusal: "reason is longer than 1000 characters",
		},
		{
			name: "params with a key twice", route: "payments", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {"board": "ops", "board": "secret", "title": "t"}, "reason": "x"}`),
			wantRefusal: `the key "board" is given twice`,
		},
		{
			name: "params that break the schema", route: "payments", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {"board": "ops"}, "reason": "x"}`),
			wantRefusal: "params:",
		},
		{
			name: "params outside the limits", route: "payments", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {"board": "finance", "title": "t"}, "reason": "x"}`),
			wantRefusal: `action monitor-note refused these parameters: board "finance": outside configured limits`,
		},
		{
			name: "a failed validation of the action", route: "payments", mode: action.Approved,
			input:       input(`{"action": "monitor-note", "params": {"board": "ops", "title": "t"}, "reason": "x"}`),
			validateErr: context.DeadlineExceeded,
			wantRefusal: "refused these parameters: context deadline exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			note := newFakeNote("monitor-note")
			note.validateErr = tt.validateErr

			s := offline(t, policy, note, newFakeNote("other-note"))

			got, refusal := s.prepare(t.Context(), ProposeRequest{InvestigationID: uuid.New(), ToolCallID: "call-1", Route: tt.route, Mode: tt.mode, Input: tt.input})
			if tt.wantRefusal != "" {
				require.Contains(t, refusal, tt.wantRefusal)

				return
			}

			require.Empty(t, refusal)
			require.Equal(t, "monitor-note", got.action)
			require.JSONEq(t, tt.wantParams, string(got.params))
			require.Equal(t, tt.wantReason, got.reason)
		})
	}
}

func TestPrepareFixKind(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"api", "pr", "odd", "plain", "monitor-note"}}}
	input := func(name, params string) json.RawMessage {
		return json.RawMessage(`{"action": "` + name + `", "params": ` + params + `, "reason": "Too noisy."}`)
	}

	tests := []struct {
		name  string
		input json.RawMessage
		want  action.FixKind
	}{
		{name: "an action that fixes through an API", input: input("api", `{"threshold": 90}`), want: action.FixAPI},
		{name: "an action that fixes through a pull request", input: input("pr", `{"threshold": 90}`), want: action.FixPullRequest},
		{name: "a kind that is not known is stored as none", input: input("odd", `{"threshold": 90}`)},
		{name: "an action that changes a definition and fixes nothing", input: input("plain", `{"threshold": 90}`)},
		{name: "an action that is not a monitor", input: input("monitor-note", `{"board": "ops", "title": "t"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := offlineWith(t, policy, map[string]action.Action{
				"api":          fixingMonitor{fakeMonitor: newFakeMonitor("api"), kind: action.FixAPI},
				"pr":           fixingMonitor{fakeMonitor: newFakeMonitor("pr"), kind: action.FixPullRequest},
				"odd":          fixingMonitor{fakeMonitor: newFakeMonitor("odd"), kind: "mute"},
				"plain":        newFakeMonitor("plain"),
				"monitor-note": newFakeNote("monitor-note"),
			})

			got, refusal := s.prepare(t.Context(), ProposeRequest{InvestigationID: uuid.New(), ToolCallID: "call-1", Route: "payments", Mode: action.Approved, Input: tt.input})
			require.Empty(t, refusal)
			require.Equal(t, tt.want, got.fixKind)
		})
	}
}

func TestFixKindText(t *testing.T) {
	t.Parallel()

	require.Nil(t, fixKindText(""))
	require.Equal(t, "pull_request", *fixKindText(action.FixPullRequest))
}

func TestPrepareSnapshot(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"monitor", "monitor-note"}}}

	call := func(name, params string) json.RawMessage {
		return json.RawMessage(`{"action": "` + name + `", "params": ` + params + `, "reason": "Too noisy."}`)
	}

	tests := []struct {
		name         string
		input        json.RawMessage
		readErr      error
		wantRefusal  string
		wantSnapshot bool
		wantUndoable bool
	}{
		{
			name: "a change the limits allow", input: call("monitor", `{"threshold": 90}`),
			wantSnapshot: true, wantUndoable: true,
		},
		{
			name: "a field outside the limits", input: call("monitor", `{"threshold": 90, "message": "New text"}`),
			wantRefusal: `action monitor refused this change: check the fields: the field "message" is not one of limits.fields`,
		},
		{
			name: "a change that changes nothing", input: call("monitor", `{"threshold": 80}`),
			wantRefusal: "action monitor: the change leaves the definition as it is",
		},
		{
			name: "a definition that cannot be read", input: call("monitor", `{"threshold": 90}`), readErr: errors.New("connection refused"),
			wantRefusal: "could not read the current definition of the target, so nothing was proposed; try again later: connection refused",
		},
		{
			name: "a limit hit while reading", input: call("monitor", `{"threshold": 90}`), readErr: action.ErrLimit,
			wantRefusal: "action monitor refused this change: outside configured limits",
		},
		{name: "parameters outside the limits stop before the snapshot", input: call("monitor", `{"threshold": 900}`), readErr: errors.New("never read"), wantRefusal: "refused these parameters"},
		{name: "an action with no snapshot", input: call("monitor-note", `{"board": "ops", "title": "t"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			monitor := newFakeMonitor("monitor")
			monitor.readErr = tt.readErr

			s := offlineWith(t, policy, map[string]action.Action{"monitor": monitor, "monitor-note": newFakeNote("monitor-note")})

			got, refusal := s.prepare(t.Context(), ProposeRequest{InvestigationID: uuid.New(), ToolCallID: "call-1", Route: "payments", Mode: action.Approved, Input: tt.input})
			if tt.wantRefusal != "" {
				require.Contains(t, refusal, tt.wantRefusal)

				return
			}

			require.Empty(t, refusal)
			require.Equal(t, tt.wantSnapshot, got.snapshot != nil)
			require.Equal(t, tt.wantUndoable, got.undoable)

			if !tt.wantSnapshot {
				return
			}

			var snap action.Snapshot

			require.NoError(t, json.Unmarshal(got.snapshot, &snap))
			require.Equal(t, "fake:monitor/1", snap.Target)
			require.Equal(t, "1", snap.Version)
			require.JSONEq(t, startMonitor, string(snap.Before))
			require.JSONEq(t, `{"message":"Checkout is slow","options":{"critical":90,"notify_no_data":false},"tags":["team:payments"]}`, string(snap.After))
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}

	return string(out)
}

func TestProposalID(t *testing.T) {
	t.Parallel()

	inv := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

	first, again := proposalID(inv, "call-1"), proposalID(inv, "call-1")

	require.Equal(t, first, again, "a repeated call finds its proposal")
	require.NotEqual(t, proposalID(inv, "call-1"), proposalID(inv, "call-2"))
	require.NotEqual(t, proposalID(inv, "call-1"), proposalID(uuid.New(), "call-1"))
}

func TestCut(t *testing.T) {
	t.Parallel()

	require.Equal(t, "abc", cut("abc", 3))
	require.Equal(t, "ab…", cut("abcd", 3))
	require.Equal(t, "日本…", cut("日本語です", 3))
}

// checkedNote is a fake note that also checks proposals the way an action
// that calls another service does.
type checkedNote struct {
	*fakeNote

	checkErr error
	checks   atomic.Int32
}

var _ action.ProposalChecker = (*checkedNote)(nil)

func (c *checkedNote) CheckProposal(context.Context, json.RawMessage) error {
	c.checks.Add(1)

	return c.checkErr
}

// slowNote is a fake note whose check waits for its context to end.
type slowNote struct {
	*fakeNote
}

var _ action.ProposalChecker = (*slowNote)(nil)

func (slowNote) CheckProposal(ctx context.Context, _ json.RawMessage) error {
	<-ctx.Done()

	return fmt.Errorf("the check was cut off: %w", ctx.Err())
}

func TestPrepareProposalCheckerTimesOut(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"monitor-note"}}}

	s, err := New(Options{Pool: new(pgxpool.Pool), Actions: map[string]action.Action{"monitor-note": slowNote{newFakeNote("monitor-note")}}, Policy: policy})
	require.NoError(t, err)
	require.Equal(t, CheckTimeout, s.checkTimeout)

	s.checkTimeout = 20 * time.Millisecond

	input := json.RawMessage(`{"action": "monitor-note", "params": {"board": "ops", "title": "t"}, "reason": "x"}`)

	_, refusal := s.prepare(t.Context(), ProposeRequest{InvestigationID: uuid.New(), ToolCallID: "call-1", Route: "payments", Mode: action.Approved, Input: input})
	require.Equal(t, "action monitor-note could not finish checking these parameters within 20ms; propose it again", refusal)
}

func TestPrepareProposalChecker(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{"payments": {Mode: action.Approved, Actions: []string{"monitor-note"}}}

	tests := []struct {
		name        string
		board       string
		checkErr    error
		wantRefusal string
		wantChecks  int32
	}{
		{name: "a check that passes", board: "ops", wantChecks: 1},
		{
			name: "a check that refuses", board: "ops", checkErr: errors.New("prometheus/rules/a.yaml:3: bad rule"),
			wantRefusal: "action monitor-note checked these parameters and refused them: prometheus/rules/a.yaml:3: bad rule", wantChecks: 1,
		},
		{
			name: "a refusal longer than the usual cut", board: "ops", checkErr: errors.New(repeat("e", maxRefusalChars+100)),
			wantRefusal: repeat("e", maxRefusalChars+100), wantChecks: 1,
		},
		{name: "no check when the limits refuse first", board: "finance", wantRefusal: "refused these parameters", wantChecks: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			note := &checkedNote{fakeNote: newFakeNote("monitor-note"), checkErr: tt.checkErr}

			s, err := New(Options{Pool: new(pgxpool.Pool), Actions: map[string]action.Action{"monitor-note": note}, Policy: policy})
			require.NoError(t, err)

			input := json.RawMessage(`{"action": "monitor-note", "params": {"board": "` + tt.board + `", "title": "t"}, "reason": "x"}`)

			got, refusal := s.prepare(t.Context(), ProposeRequest{InvestigationID: uuid.New(), ToolCallID: "call-1", Route: "payments", Mode: action.Approved, Input: input})
			require.Equal(t, tt.wantChecks, note.checks.Load())

			if tt.wantRefusal != "" {
				require.Contains(t, refusal, tt.wantRefusal)

				return
			}

			require.Empty(t, refusal)
			require.JSONEq(t, `{"board":"ops","title":"t"}`, string(got.params))
		})
	}
}

func TestSpecDescribesReadWriteMode(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{
		"payments": {Mode: action.ReadWrite, Actions: []string{"monitor-note"}},
		"approve":  {Mode: action.Approved, Actions: []string{"monitor-note"}},
	}
	s := offline(t, policy, newFakeNote("monitor-note"))

	tests := []struct {
		name         string
		route        string
		mode         action.Mode
		wantContains string
		wantMissing  string
	}{
		{name: "read-write", route: "payments", mode: action.ReadWrite, wantContains: "runs at once", wantMissing: "approver approves"},
		{name: "approved", route: "approve", mode: action.Approved, wantContains: "approver approves", wantMissing: "runs at once"},
		{name: "a read-write investigation on a route that is approved now", route: "approve", mode: action.ReadWrite, wantContains: "approver approves", wantMissing: "runs at once"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec, ok := s.Spec(tt.route, tt.mode)
			require.True(t, ok)
			require.Contains(t, spec.Description, tt.wantContains)
			require.NotContains(t, spec.Description, tt.wantMissing)
		})
	}
}

package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

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

	s, err := New(Options{Pool: new(pgxpool.Pool), Actions: byName, Policy: policy})
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

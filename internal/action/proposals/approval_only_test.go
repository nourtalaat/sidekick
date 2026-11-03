package proposals

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// gatedName is the name of the fake approval-only action.
const gatedName = "rollback"

// fakeGatedNote is the fake note action as an approval-only action. Its
// gate is what the config says now, which a test changes while it runs.
type fakeGatedNote struct {
	*fakeNote

	mu   sync.Mutex
	gate action.ReadWriteGate
}

var _ action.ApprovalOnly = (*fakeGatedNote)(nil)

func newFakeGatedNote(name string, allowReadWrite bool) *fakeGatedNote {
	return &fakeGatedNote{fakeNote: newFakeNote(name), gate: action.ReadWriteGate{AllowReadWrite: allowReadWrite}}
}

func (g *fakeGatedNote) ReadWriteAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.gate.ReadWriteAllowed()
}

func (g *fakeGatedNote) allowReadWrite(allowed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.gate.AllowReadWrite = allowed
}

func TestSpecNotesApprovalOnlyActionsInReadWriteMode(t *testing.T) {
	t.Parallel()

	policy := action.StaticPolicy{
		"payments": {Mode: action.ReadWrite, Actions: []string{"monitor-note", gatedName, "opted-in"}},
		"review":   {Mode: action.Approved, Actions: []string{"monitor-note", gatedName}},
	}

	tests := []struct {
		name      string
		route     string
		mode      action.Mode
		wantNotes int
	}{
		{name: "read-write marks only the action that is blocked", route: "payments", mode: action.ReadWrite, wantNotes: 1},
		{name: "approved mode needs no note", route: "review", mode: action.Approved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := offlineWith(t, policy, map[string]action.Action{
				"monitor-note": newFakeNote("monitor-note"),
				gatedName:      newFakeGatedNote(gatedName, false),
				"opted-in":     newFakeGatedNote("opted-in", true),
			})

			spec, ok := s.Spec(tt.route, tt.mode)
			require.True(t, ok)
			require.Equal(t, tt.wantNotes, strings.Count(spec.Description, approvalOnlyNote))
		})
	}
}

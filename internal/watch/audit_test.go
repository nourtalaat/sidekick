package watch

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestAuditEntry(t *testing.T) {
	t.Parallel()

	responder := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")
	running := db.Watcher{ID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d1"), Name: "health", Status: StatusRunning}

	ended := func(status, reason string, by *uuid.UUID) db.Watcher {
		w := running
		w.Status, w.StopReason, w.StoppedBy = status, &reason, by

		return w
	}

	tests := []struct {
		name       string
		old        *db.Watcher
		w          db.Watcher
		wantActor  string
		wantID     string
		wantAction string
	}{
		{name: "a start", w: running, wantActor: audit.ActorSidekick, wantAction: "watcher.started"},
		{name: "a recovery", old: &running, w: ended(StatusRecovered, "", nil), wantActor: audit.ActorSystem, wantAction: "watcher.recovered"},
		{name: "a timeout", old: &running, w: ended(StatusTimedOut, "", nil), wantActor: audit.ActorSystem, wantAction: "watcher.timed_out"},
		{
			name:       "a responder stops it",
			old:        &running,
			w:          ended(StatusStopped, StopResponder, &responder),
			wantActor:  audit.ActorUser,
			wantID:     responder.String(),
			wantAction: "watcher.stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := auditEntry(tt.old, tt.w)
			require.Equal(t, tt.wantActor, e.ActorKind)
			require.Equal(t, tt.wantID, e.ActorID)
			require.Equal(t, tt.wantAction, e.Action)
			require.Equal(t, "watcher", e.TargetKind)
			require.Equal(t, tt.w.ID.String(), e.TargetID)
			require.Equal(t, viewOf(tt.w), e.New)

			if tt.old == nil {
				require.Nil(t, e.Old)
			} else {
				require.Equal(t, viewOf(*tt.old), e.Old)
			}
		})
	}
}

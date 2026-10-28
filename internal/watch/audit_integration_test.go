//go:build integration

package watch

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestStateChangesAreAudited(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// end ends the started watcher, or is nil to leave it running.
		end  func(t *testing.T, h *harness)
		want []auditRow
	}{
		{
			name: "a watcher start writes one row",
			want: []auditRow{{ActorKind: audit.ActorSidekick, Action: "watcher.started", Status: StatusRunning}},
		},
		{
			name: "a watcher ended when its group resolves writes one more",
			end: func(t *testing.T, h *harness) {
				t.Helper()

				require.NoError(t, pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
					_, err := EndGroupResolved(t.Context(), tx, checkoutGroup, h.now, h.recorder)

					return err
				}))
			},
			want: []auditRow{
				{ActorKind: audit.ActorSidekick, Action: "watcher.started", Status: StatusRunning},
				{ActorKind: audit.ActorSystem, Action: "watcher.stopped", OldStatus: StatusRunning, Status: StatusStopped},
			},
		},
		{
			name: "a watcher a responder stops in Slack has the responder as actor",
			end: func(t *testing.T, h *harness) {
				t.Helper()

				_, stopped, err := h.stopper(t).Stop(t.Context(), h.watcherByName(t, "health").ID, output.Person{SlackUserID: stopper})
				require.NoError(t, err)
				require.True(t, stopped)
			},
			want: []auditRow{
				{ActorKind: audit.ActorSidekick, Action: "watcher.started", Status: StatusRunning},
				{ActorKind: audit.ActorUser, ActorID: stopper, Action: "watcher.stopped", OldStatus: StatusRunning, Status: StatusStopped},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			w := h.mustStart(t, httpWatcher("health", "http://127.0.0.1/healthz", 2))

			if tt.end != nil {
				tt.end(t, h)
			}

			got := auditRows(t, h)
			for i := range got {
				require.Equal(t, "watcher:"+w.ID.String(), got[i].Target)
				require.Equal(t, &checkoutInvestigation, got[i].InvestigationID)
				got[i].Target, got[i].InvestigationID = "", nil
			}

			require.Equal(t, tt.want, got)
		})
	}
}

// auditRow is an audit log row as the tests compare it.
type auditRow struct {
	ActorKind       string
	ActorID         string
	Action          string
	Target          string
	InvestigationID *uuid.UUID
	OldStatus       string
	Status          string
}

// auditRows returns the audit log, oldest first.
func auditRows(t *testing.T, h *harness) []auditRow {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT actor_kind, coalesce(actor_id, ''), action, target_kind || ':' || target_id, investigation_id, details FROM audit_log ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []auditRow

	for rows.Next() {
		var (
			r       auditRow
			details []byte
		)

		require.NoError(t, rows.Scan(&r.ActorKind, &r.ActorID, &r.Action, &r.Target, &r.InvestigationID, &details))

		var d struct {
			Old *watcherView `json:"old"`
			New watcherView  `json:"new"`
		}

		require.NoError(t, json.Unmarshal(details, &d))
		require.Equal(t, "health", d.New.Name)

		r.Status = d.New.Status
		if d.Old != nil {
			r.OldStatus = d.Old.Status
		}

		out = append(out, r)
	}

	require.NoError(t, rows.Err())

	return out
}

// watcherByName returns the watcher of the checkout group named name.
func (h *harness) watcherByName(t *testing.T, name string) db.Watcher {
	t.Helper()

	w, err := db.New(h.pool).GetWatcherByName(t.Context(), db.GetWatcherByNameParams{GroupID: checkoutGroup, Name: name})
	require.NoError(t, err)

	return w
}

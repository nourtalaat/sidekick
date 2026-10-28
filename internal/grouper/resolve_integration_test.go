//go:build integration

package grouper

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/watch"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	missing := uuid.MustParse("0192d3a4-0000-7000-8000-00000000ffff")

	tests := []struct {
		name string
		// prepare are the fixtures loaded first.
		prepare []string
		// before changes the group before the first Resolve.
		before  string
		groupID uuid.UUID
		// resolves is how many times Resolve is called.
		resolves     int
		wantResolved []bool
		wantErr      error
		wantStatus   string
		wantReason   *string
		wantWatchers map[string]string
		wantAudit    int
	}{
		{
			name:         "an open group with running watchers",
			prepare:      []string{runningHTTPWatcher, runningAlertWatcher},
			groupID:      diskGroup,
			resolves:     1,
			wantResolved: []bool{true},
			wantStatus:   "closed",
			wantReason:   new(ClosedResolved),
			wantWatchers: map[string]string{"health": watch.StatusStopped, "cleared": watch.StatusRecovered},
			wantAudit:    1,
		},
		{
			name:         "resolving twice changes nothing the second time",
			groupID:      diskGroup,
			resolves:     2,
			wantResolved: []bool{true, false},
			wantStatus:   "closed",
			wantReason:   new(ClosedResolved),
			wantWatchers: map[string]string{},
			wantAudit:    1,
		},
		{
			name:         "a group that closed when its window ended",
			before:       "UPDATE event_groups SET status = 'closed', closed_reason = 'window_ended'",
			groupID:      diskGroup,
			resolves:     1,
			wantResolved: []bool{false},
			wantErr:      ErrGroupClosed,
			wantStatus:   "closed",
			wantReason:   new(ClosedWindowEnded),
			wantWatchers: map[string]string{},
		},
		{
			name:         "a group that does not exist",
			groupID:      missing,
			resolves:     1,
			wantResolved: []bool{false},
			wantErr:      ErrGroupNotFound,
			wantStatus:   "open",
			wantWatchers: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			for _, fixture := range tt.prepare {
				storetest.LoadFixture(t, h.pool, fixture)
			}

			if tt.before != "" {
				_, err := h.pool.Exec(t.Context(), tt.before)
				require.NoError(t, err)
			}

			by := Actor{Kind: audit.ActorUser, ID: "0192d3a4-0000-7000-8000-0000000000a1"}
			now := seededWindowEnd

			for i := range tt.resolves {
				var resolved bool

				err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
					var err error

					resolved, err = Resolve(t.Context(), tx, tt.groupID, by, now, nil)
					if err != nil {
						return fmt.Errorf("resolve: %w", err)
					}

					return nil
				})

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				} else {
					require.NoError(t, err)
				}

				require.Equal(t, tt.wantResolved[i], resolved)
			}

			grp := h.groupByID(t, diskGroup)
			require.Equal(t, tt.wantStatus, grp.Status)
			require.Equal(t, tt.wantReason, grp.ClosedReason)
			require.Equal(t, tt.wantWatchers, h.watcherStatuses(t))

			var (
				count   int
				kind    string
				actorID string
				target  string
			)

			require.NoError(t, h.pool.QueryRow(t.Context(),
				"SELECT count(*), coalesce(max(actor_kind), ''), coalesce(max(actor_id), ''), coalesce(max(target_id), '') FROM audit_log WHERE action = $1", ActionResolved).
				Scan(&count, &kind, &actorID, &target))
			require.Equal(t, tt.wantAudit, count)

			if tt.wantAudit > 0 {
				require.Equal(t, audit.ActorUser, kind)
				require.Equal(t, by.ID, actorID)
				require.Equal(t, diskInvestigation.String(), target, "the row names the group's investigation")
			}
		})
	}
}

func TestResolveRecordsTheEndedWatchers(t *testing.T) {
	t.Parallel()

	h := setup(t)
	storetest.LoadFixture(t, h.pool, runningHTTPWatcher)

	err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
		_, err := Resolve(t.Context(), tx, diskGroup, Actor{Kind: audit.ActorToken, ID: "token-1"}, time.Now(), nil)
		if err != nil {
			return fmt.Errorf("resolve: %w", err)
		}

		return nil
	})
	require.NoError(t, err)

	var count int

	require.NoError(t, h.pool.QueryRow(t.Context(),
		"SELECT count(*) FROM audit_log WHERE action = 'watcher.stopped'").Scan(&count))
	require.Equal(t, 1, count, "the watcher's end is audited like a recovery's")
}

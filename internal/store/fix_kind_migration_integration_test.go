//go:build integration

package store_test

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// fixKindMigration is the end of the name of the migration that adds
// action_proposals.fix_kind.
const fixKindMigration = "_add_action_proposal_fix_kind.sql"

const (
	fixKindInvestigation = "0192d3a4-0000-7000-8000-0000000000c1"
	fixKindOriginal      = "0192d3a4-0000-7000-8000-0000000000a1"
	fixKindUndo          = "0192d3a4-0000-7000-8000-0000000000a2"
)

// TestFixKindBackfill applies the migrations before the one that adds
// fix_kind, stores proposals with each kind of snapshot target, applies the
// rest, and checks which proposals got a kind.
func TestFixKindBackfill(t *testing.T) {
	t.Parallel()

	pool, err := store.Open(t.Context(), storetest.NewEmptyURL(t), schemaMaxConns)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, store.MigrateFS(t.Context(), pool, migrationsBefore(t, fixKindMigration)))

	_, err = pool.Exec(t.Context(), `
		INSERT INTO event_groups (id, fingerprint, route, service, status, event_count, opened_at, last_event_at, investigation_id)
		VALUES ('0192d3a4-0000-7000-8000-0000000000b1', 'fp', 'payments', 'checkout-api', 'open', 1, now(), now(), '`+fixKindInvestigation+`');
		INSERT INTO investigations (id, group_id, route, mode, status, connectors, budget, created_at)
		VALUES ('`+fixKindInvestigation+`', '0192d3a4-0000-7000-8000-0000000000b1', 'payments', 'approved', 'running', '{}', '{}', now())`)
	require.NoError(t, err)

	tests := []struct {
		name     string
		snapshot string
		want     *string
	}{
		{name: "a Datadog monitor", snapshot: snapshotOfTarget("datadog:monitor/1"), want: new("api")},
		{name: "a SigNoz rule", snapshot: snapshotOfTarget("signoz:rule/a1"), want: new("api")},
		{name: "a Grafana rule", snapshot: snapshotOfTarget("grafana:alert_rule/u1"), want: new("api")},
		{name: "a Prometheus rule", snapshot: snapshotOfTarget("kubernetes:prometheusrule/monitoring/rules"), want: new("api")},
		{name: "a pull request", snapshot: snapshotOfTarget("github:acme/monitors"), want: new("pull_request")},
		{name: "a rollback of a deployment", snapshot: snapshotOfTarget("kubernetes:deployment/prod/checkout")},
		{name: "a target that only looks like a Grafana rule", snapshot: snapshotOfTarget("grafanaXalert_rule/u1")},
		{name: "a proposal with no snapshot", snapshot: "NULL"},
	}

	ids := make([]string, len(tests))

	for i, tt := range tests {
		ids[i] = fmt.Sprintf("0192d3a4-0000-7000-8000-%012x", 0x100+i)

		_, err := pool.Exec(t.Context(), `INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, snapshot)
			VALUES ($1, $2, 'act', '{}', 'approved', 'succeeded', $3, now(), now(), `+tt.snapshot+`)`, ids[i], fixKindInvestigation, "key-"+ids[i])
		require.NoError(t, err, tt.name)
	}

	_, err = pool.Exec(t.Context(), `INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, snapshot)
		VALUES ($1, $2, 'act', '{}', 'approved', 'succeeded', $3, now(), now(), `+snapshotOfTarget("datadog:monitor/1")+`)`, fixKindOriginal, fixKindInvestigation, "key-original")
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `INSERT INTO action_proposals (id, investigation_id, action, params, mode, status, idempotency_key, proposed_at, expires_at, snapshot, undo_of)
		VALUES ($1, $2, 'act', '{}', 'approved', 'succeeded', $4, now(), now(), `+snapshotOfTarget("datadog:monitor/1")+`, $3)`, fixKindUndo, fixKindInvestigation, fixKindOriginal, "key-undo")
	require.NoError(t, err)

	require.NoError(t, store.Migrate(t.Context(), pool))

	for i, tt := range tests {
		require.Equal(t, tt.want, fixKindOf(t, pool, ids[i]), tt.name)
	}

	require.Equal(t, new("api"), fixKindOf(t, pool, fixKindOriginal), "the proposal an undo restores")
	require.Nil(t, fixKindOf(t, pool, fixKindUndo), "an undo copies the target of the proposal it restores")
}

// snapshotOfTarget returns an SQL literal for a snapshot with target.
func snapshotOfTarget(target string) string {
	return fmt.Sprintf(`'{"target": %q, "before": {}, "after": {"a": 1}}'::jsonb`, target)
}

// fixKindOf returns fix_kind of the proposal with id.
func fixKindOf(t *testing.T, pool *pgxpool.Pool, id string) *string {
	t.Helper()

	var kind *string

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT fix_kind FROM action_proposals WHERE id = $1", id).Scan(&kind))

	return kind
}

// migrationsBefore returns the migrations that come before the one whose
// file name ends in suffix.
func migrationsBefore(t *testing.T, suffix string) fs.FS {
	t.Helper()

	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)

	var limit int64

	for _, file := range files {
		if strings.HasSuffix(file, suffix) {
			limit, err = goose.NumericComponent(file)
			require.NoError(t, err)
		}
	}

	require.NotZero(t, limit, "no migration ends in %s", suffix)

	before := fstest.MapFS{}

	for _, file := range files {
		version, err := goose.NumericComponent(file)
		require.NoError(t, err)

		if version >= limit {
			continue
		}

		content, err := fs.ReadFile(migrations.FS, file)
		require.NoError(t, err)

		before[file] = &fstest.MapFile{Data: content}
	}

	return before
}

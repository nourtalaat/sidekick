//go:build integration

package rollup

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The file of the migration that adds the pull request state trigger, found
// by name so that a new number does not break the test, and the start of the
// statement in it that marks the hours of the history.
const (
	markMigrationGlob  = "*_add_pull_request_state_rollup_triggers.sql"
	markStatementStart = "SELECT rollup_mark('stats_hourly', min("
)

func TestTheMigrationMarksTheHoursTheTriggersWould(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/fix_marks.sql")

	// Loading the fixture marks the hour of every investigation and proposal.
	byTriggers := dirtyOf(t, pool)

	require.Equal(t, []string{
		"stats_hourly 2026-10-20 10:00",
		"stats_hourly 2026-10-20 11:00",
		"stats_hourly 2026-10-20 15:00",
		"stats_hourly 2026-10-20 16:00",
		"stats_hourly 2026-10-21 22:00",
	}, byTriggers)

	_, err := pool.Exec(t.Context(), "DELETE FROM rollup_dirty")
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), markStatement(t))
	require.NoError(t, err)

	byMigration := dirtyOf(t, pool)

	require.Equal(t, []string{
		"stats_hourly 2026-10-20 10:00",
		"stats_hourly 2026-10-20 11:00",
		"stats_hourly 2026-10-21 22:00",
	}, byMigration)
	require.Subset(t, byTriggers, byMigration)
}

// markStatement returns the statement of the migration that marks the hours
// of the history, taken from the embedded file so that the test runs the
// statement the migration runs.
func markStatement(t *testing.T) string {
	t.Helper()

	files, err := fs.Glob(migrations.FS, markMigrationGlob)
	require.NoError(t, err)
	require.Len(t, files, 1)

	body, err := fs.ReadFile(migrations.FS, files[0])
	require.NoError(t, err)

	_, rest, found := strings.Cut(string(body), markStatementStart)
	require.True(t, found)

	statement, _, found := strings.Cut(rest, ";")
	require.True(t, found)

	return markStatementStart + statement
}

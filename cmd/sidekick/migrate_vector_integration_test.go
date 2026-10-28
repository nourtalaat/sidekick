//go:build integration

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// vectorConfig is validConfig with vector memory on, for vectors of the
// number of dimensions that DIMENSIONS is replaced with.
const vectorConfig = validConfig + `
memory:
  vector:
    enabled: true
    embeddings: { provider: voyage, model: voyage-4, dimensions: DIMENSIONS }
`

// runMigrate runs `sidekick migrate` with args, giving it stdin as the
// input, and returns what it printed.
func runMigrate(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	var out bytes.Buffer

	root := newTestRootCmd(newMigrateCmd(fakeRegistries()))
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"migrate"}, args...))

	err := root.Execute()

	return out.String(), err
}

func TestMigrateVectorCommands(t *testing.T) {
	t.Parallel()

	dbURL := storetest.NewVectorEmptyURL(t)

	three := writeConfig(t, strings.ReplaceAll(vectorConfig, "DIMENSIONS", "3"), dbURL)
	four := writeConfig(t, strings.ReplaceAll(vectorConfig, "DIMENSIONS", "4"), dbURL)

	pool, err := store.Open(t.Context(), dbURL, 2)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	typmod := func() int {
		var n int
		require.NoError(t, pool.QueryRow(t.Context(),
			"SELECT atttypmod FROM pg_attribute WHERE attrelid = 'memory_embeddings'::regclass AND attname = 'embedding'").Scan(&n))

		return n
	}

	// up applies the main set and the vector set, for 3 dimensions.
	out, err := runMigrate(t, "", "up", "--config", three)
	require.NoError(t, err)
	require.Contains(t, out, "The database has every migration.")
	require.Equal(t, 3, typmod())

	// status lists both sets.
	out, err = runMigrate(t, "", "status", "--config", three)
	require.NoError(t, err)
	require.Contains(t, out, "00001_baseline.sql")
	require.Contains(t, out, "Vector memory migrations")
	require.Contains(t, out, "00001_add_memory_embeddings.sql")

	// Another length is refused, with the way to reset.
	_, err = runMigrate(t, "", "up", "--config", four)
	require.ErrorContains(t, err, "memory_embeddings holds 3-dimension vectors but memory.vector.embeddings.dimensions is 4")
	require.ErrorContains(t, err, "sidekick migrate vector-reset")

	// A reset that is not confirmed changes nothing.
	_, err = runMigrate(t, "no\n", "vector-reset", "--config", four)
	require.ErrorIs(t, err, errResetDeclined)
	require.Equal(t, 3, typmod())
	require.Zero(t, auditRows(t, pool))

	// Confirmed with --yes, it empties the table and writes one audit entry.
	out, err = runMigrate(t, "", "vector-reset", "--yes", "--config", four)
	require.NoError(t, err)
	require.Contains(t, out, "which held vectors of 3 dimensions")
	require.Equal(t, 1, auditRows(t, pool))

	var actor, old, updated string
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT actor_kind, details->'old'->>'dimensions', details->'new'->>'dimensions' FROM audit_log WHERE action = 'memory.vectors_reset'").
		Scan(&actor, &old, &updated))
	require.Equal(t, "system", actor)
	require.Equal(t, "3", old)
	require.Equal(t, "4", updated)

	// Then up gives the column the new length.
	_, err = runMigrate(t, "", "up", "--config", four)
	require.NoError(t, err)
	require.Equal(t, 4, typmod())

	// Answering yes at the question resets too.
	out, err = runMigrate(t, "yes\n", "vector-reset", "--config", four)
	require.NoError(t, err)
	require.Contains(t, out, "Type yes to continue")
	require.Equal(t, 2, auditRows(t, pool))
}

func TestMigrateVectorResetNeedsVectorMemory(t *testing.T) {
	t.Parallel()

	configPath := writeConfig(t, validConfig, storetest.NewEmptyURL(t))

	_, err := runMigrate(t, "", "vector-reset", "--yes", "--config", configPath)
	require.ErrorIs(t, err, errVectorsOff)
}

// auditRows counts the entries of vector resets in the audit log.
func auditRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE action = 'memory.vectors_reset'").Scan(&n))

	return n
}

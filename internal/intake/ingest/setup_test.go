//go:build integration

package ingest

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Writer on a database of its own.
type harness struct {
	pool   *pgxpool.Pool
	writer *Writer
}

// setup returns a Writer on a new database seeded with fixture/existing-event.sql.
func setup(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/existing-event.sql")

	return &harness{pool: pool, writer: NewWriter(pool, testRedactor(t))}
}

// events returns every stored event, oldest first.
func (h *harness) events(t *testing.T) []db.Event {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM events ORDER BY id")
	require.NoError(t, err)

	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.Event])
	require.NoError(t, err)

	return events
}

// routeJobs returns the event IDs in the payloads of the route_event jobs,
// oldest first.
func (h *harness) routeJobs(t *testing.T) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT payload->>'event_id' FROM jobs WHERE kind = 'route_event' ORDER BY id")
	require.NoError(t, err)

	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return ids
}

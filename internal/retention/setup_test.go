//go:build integration

package retention

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// testNow is the time of the runs in the tests, which
// fixture/records.sql is written for.
var testNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// testPeriods are the periods fixture/records.sql is written for.
var testPeriods = Periods{
	Events:         10 * 24 * time.Hour,
	Investigations: 30 * 24 * time.Hour,
	Steps:          5 * 24 * time.Hour,
	AuditLog:       20 * 24 * time.Hour,
	WatcherChecks:  3 * 24 * time.Hour,
}

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Handler at testNow on a database of its own seeded with
// fixture/records.sql.
type harness struct {
	pool    *pgxpool.Pool
	handler *Handler
	metrics *Metrics
}

// setup returns a harness whose batches take at most batch rows.
func setup(t *testing.T, batch int32) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/records.sql")

	metrics, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	return &harness{
		pool:    pool,
		metrics: metrics,
		handler: New(Options{
			Pool:      pool,
			Periods:   testPeriods,
			BatchSize: batch,
			Metrics:   metrics,
			Now:       func() time.Time { return testNow },
		}),
	}
}

// strings returns the single text column of every row of query, in order.
func (h *harness) strings(t *testing.T, query string) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), query)
	require.NoError(t, err)

	var out []string

	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))

		out = append(out, s)
	}

	require.NoError(t, rows.Err())

	return out
}

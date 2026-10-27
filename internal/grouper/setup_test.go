//go:build integration

package grouper

import (
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of fixture/groups.sql.
var (
	diskGroup         = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000b1")
	diskInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	diskRouted        = uuid.MustParse("0192d3a4-0000-7000-8000-000000000002")
	cpuRouted         = uuid.MustParse("0192d3a4-0000-7000-8000-000000000003")
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Grouper on a database of its own, seeded with
// fixture/groups.sql.
type harness struct {
	pool    *pgxpool.Pool
	grouper *Grouper
	spans   *tracetest.SpanRecorder
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/groups.sql")

	spans := tracetest.NewSpanRecorder()

	return &harness{
		pool:    pool,
		grouper: New(Options{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))}),
		spans:   spans,
	}
}

// The rows of fixture/related.sql.
var (
	dbGroup             = uuid.MustParse("0192d3a4-0000-7000-8000-0000000001b1")
	dbInvestigation     = uuid.MustParse("0192d3a4-0000-7000-8000-0000000001c1")
	dbConnectionsRouted = uuid.MustParse("0192d3a4-0000-7000-8000-000000000102")
	checkoutRouted      = uuid.MustParse("0192d3a4-0000-7000-8000-000000000103")
	searchRouted        = uuid.MustParse("0192d3a4-0000-7000-8000-000000000104")
	noServiceRouted     = uuid.MustParse("0192d3a4-0000-7000-8000-000000000105")
)

// setupRelated returns a Grouper with catalog on a database of its own,
// seeded with fixture/related.sql.
func setupRelated(t *testing.T, catalog *Catalog) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/related.sql")

	spans := tracetest.NewSpanRecorder()

	return &harness{
		pool:    pool,
		grouper: New(Options{TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)), Catalog: catalog}),
		spans:   spans,
	}
}

// group runs Group for the event with id in its own transaction.
func (h *harness) group(t *testing.T, id uuid.UUID, rt Route) (Result, error) {
	t.Helper()

	var res Result

	err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
		ev, err := db.New(tx).GetEvent(t.Context(), id)
		require.NoError(t, err)

		res, err = h.grouper.Group(t.Context(), tx, ev, rt)

		return err
	})
	if err != nil {
		return Result{}, fmt.Errorf("group in a transaction: %w", err)
	}

	return res, nil
}

// groupByID returns the group with id.
func (h *harness) groupByID(t *testing.T, id uuid.UUID) db.EventGroup {
	t.Helper()

	var g db.EventGroup

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM event_groups WHERE id = $1", id)
	require.NoError(t, err)

	g, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[db.EventGroup])
	require.NoError(t, err)

	return g
}

// count runs a count query.
func (h *harness) count(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), query, args...).Scan(&n))

	return n
}

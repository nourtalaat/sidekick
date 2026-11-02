//go:build integration

package slack

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The investigations of fixture/investigations.sql.
const (
	slackAlertWithThread  = "0192d3a4-0000-7000-8000-0000000000c1"
	slackAlertInThread    = "0192d3a4-0000-7000-8000-0000000000c2"
	webhookAlertWithPost  = "0192d3a4-0000-7000-8000-0000000000c3"
	webhookAlertNoMessage = "0192d3a4-0000-7000-8000-0000000000c4"
	webhookThenMention    = "0192d3a4-0000-7000-8000-0000000000c5"
	personPostedAlert     = "0192d3a4-0000-7000-8000-0000000000c6"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a database with the investigations of the fixture.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	return pool
}

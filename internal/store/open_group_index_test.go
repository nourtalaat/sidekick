//go:build integration

package store_test

import (
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// openGroupIndexSuffix ends the file name of the migration that adds the
// unique index on open groups.
const openGroupIndexSuffix = "_add_open_group_fingerprint_index.sql"

// uniqueViolation is the Postgres error code unique_violation.
const uniqueViolation = "23505"

// TestOpenGroupIndexOnDuplicateGroups migrates a database to the version
// before the unique index on open groups, seeds two open groups with the
// same fingerprint as the one-group-per-event routing left them, and then
// migrates the rest of the way.
func TestOpenGroupIndexOnDuplicateGroups(t *testing.T) {
	t.Parallel()

	pool := groupsBeforeIndex(t)
	require.NoError(t, store.Migrate(t.Context(), pool))

	tests := []struct {
		name       string
		id         string
		wantStatus string
		wantReason *string
	}{
		{name: "the older duplicate is superseded", id: "0192d3a4-0000-7000-8000-0000000000a1", wantStatus: "closed", wantReason: ptr("superseded")},
		{name: "the newest duplicate stays open", id: "0192d3a4-0000-7000-8000-0000000000a2", wantStatus: "open"},
		{name: "a closed group keeps its reason", id: "0192d3a4-0000-7000-8000-0000000000a3", wantStatus: "closed", wantReason: ptr("window_ended")},
		{name: "an open group with another fingerprint stays open", id: "0192d3a4-0000-7000-8000-0000000000b1", wantStatus: "open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				status string
				reason *string
			)

			err := pool.QueryRow(t.Context(), "SELECT status, closed_reason FROM event_groups WHERE id = $1", uuid.MustParse(tt.id)).Scan(&status, &reason)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}

// TestOpenGroupIndexRejectsASecondOpenGroup checks the index after the
// migration: a second open group with a fingerprint fails, and a closed one
// does not.
func TestOpenGroupIndexRejectsASecondOpenGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        string
		wantViolation bool
	}{
		{name: "a second open group", status: "open", wantViolation: true},
		{name: "another closed group", status: "closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := groupsBeforeIndex(t)
			require.NoError(t, store.Migrate(t.Context(), pool))

			_, err := pool.Exec(t.Context(), `
				INSERT INTO event_groups (id, fingerprint, route, status, opened_at, last_event_at)
				VALUES ($1, 'fp-disk', 'default', $2, now(), now())`, uuid.New(), tt.status)

			if !tt.wantViolation {
				require.NoError(t, err)

				return
			}

			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			require.True(t, ok, "want a Postgres error, got %v", err)
			require.Equal(t, uniqueViolation, pgErr.Code)
		})
	}
}

// groupsBeforeIndex returns a database migrated to the version before the
// unique index on open groups and seeded with
// fixture/open-groups-before-index.sql.
func groupsBeforeIndex(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), storetest.NewEmptyURL(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Closing the provider closes this *sql.DB and leaves the pool open.
	provider, err := goose.NewProvider(goose.DialectPostgres, stdlib.OpenDBFromPool(pool), migrations.FS)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, provider.Close()) })

	_, err = provider.UpTo(t.Context(), versionBefore(t, openGroupIndexSuffix))
	require.NoError(t, err)

	storetest.LoadFixture(t, pool, "fixture/open-groups-before-index.sql")

	return pool
}

// versionBefore returns the version of the migration before the one whose
// file name ends with suffix.
func versionBefore(t *testing.T, suffix string) int64 {
	t.Helper()

	entries, err := fs.ReadDir(migrations.FS, ".")
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}

		version, err := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		require.NoError(t, err)

		return version - 1
	}

	require.FailNow(t, "no migration ends with "+suffix)

	return 0
}

func ptr[T any](v T) *T {
	return &v
}

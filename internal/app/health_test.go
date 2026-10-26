//go:build integration

package app

import (
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/migrations"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// emptyDB starts the app on a database without migrations.
		emptyDB bool
		mode    config.MigrationMode
		// before runs after the app started and before the request.
		before   func(t *testing.T, dbURL string)
		path     string
		onMain   bool
		wantCode int
		// wantBody is the expected body, or the fixture file that holds
		// it when it ends in .txt; DETAILS in a fixture matches any text on
		// its line. wantLines are lines the body must hold instead.
		wantBody  string
		wantLines []string
	}{
		{
			name:     "healthz",
			path:     "/healthz",
			wantCode: http.StatusOK,
			wantBody: "ok\n",
		},
		{
			name:     "healthz while the database is down",
			path:     "/healthz",
			before:   stopDatabase,
			wantCode: http.StatusOK,
			wantBody: "ok\n",
		},
		{
			name:     "readyz when ready",
			path:     "/readyz",
			wantCode: http.StatusOK,
			wantBody: "readyz-ready.txt",
		},
		{
			name:     "readyz when the database is down",
			path:     "/readyz",
			before:   stopDatabase,
			wantCode: http.StatusServiceUnavailable,
			wantBody: "readyz-database-down.txt",
		},
		{
			name:     "readyz when the schema is behind in manual mode",
			emptyDB:  true,
			mode:     config.MigrationsManual,
			path:     "/readyz",
			wantCode: http.StatusServiceUnavailable,
			wantBody: "readyz-schema-behind.txt",
		},
		{
			name:     "readyz once the schema is migrated in manual mode",
			emptyDB:  true,
			mode:     config.MigrationsManual,
			before:   migrateDatabase,
			path:     "/readyz",
			wantCode: http.StatusOK,
			wantBody: "readyz-ready.txt",
		},
		{
			name:     "readyz after start-up migrated an empty database",
			emptyDB:  true,
			path:     "/readyz",
			wantCode: http.StatusOK,
			wantBody: "readyz-ready.txt",
		},
		{
			name:     "metrics",
			path:     "/metrics",
			wantCode: http.StatusOK,
			wantLines: []string{
				`sidekick_build_info{commit="abc123",version="v0.0.0-test"} 1`,
				"# TYPE go_goroutines gauge",
			},
		},
		{
			name:     "main listener has no routes yet",
			path:     "/",
			onMain:   true,
			wantCode: http.StatusNotFound,
			wantBody: "404 page not found\n",
		},
		{
			name:     "metrics paths are not on the main listener",
			path:     "/metrics",
			onMain:   true,
			wantCode: http.StatusNotFound,
			wantBody: "404 page not found\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := storetest.NewURL(t)
			if tt.emptyDB {
				dbURL = storetest.NewEmptyURL(t)
			}

			mode := tt.mode
			if mode == "" {
				mode = config.MigrationsStartup
			}

			r := startApp(t, dbURL, mode, nil)

			if tt.before != nil {
				tt.before(t, dbURL)
			}

			base := r.metrics
			if tt.onMain {
				base = r.main
			}

			code, body := get(t, base+tt.path)
			require.Equal(t, tt.wantCode, code, body)

			for _, line := range tt.wantLines {
				require.Contains(t, strings.Split(body, "\n"), line)
			}

			if tt.wantBody != "" {
				requireBody(t, tt.wantBody, body)
			}
		})
	}
}

// requireBody checks body against want, the body itself or the name of a
// fixture file that holds it.
func requireBody(t *testing.T, want, body string) {
	t.Helper()

	if !strings.HasSuffix(want, ".txt") {
		require.Equal(t, want, body)

		return
	}

	want = strings.ReplaceAll(readFixture(t, want), "LATEST", strconv.FormatInt(latestMigration(t), 10))
	pattern := "^" + strings.ReplaceAll(regexp.QuoteMeta(want), "DETAILS", ".+") + "$"
	require.Regexp(t, pattern, body)
}

// latestMigration returns the version of the newest migration the binary
// embeds.
func latestMigration(t *testing.T) int64 {
	t.Helper()

	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	version, err := goose.NumericComponent(files[len(files)-1])
	require.NoError(t, err)

	return version
}

// stopDatabase makes the database at dbURL refuse new connections and
// closes the ones it has, as if the server went away. It works from the
// server's postgres database, since a database cannot refuse the session
// that changes it.
func stopDatabase(t *testing.T, dbURL string) {
	t.Helper()

	u, err := url.Parse(dbURL)
	require.NoError(t, err)

	name := strings.TrimPrefix(u.Path, "/")
	u.Path = "/postgres"

	conn, err := pgx.Connect(t.Context(), u.String())
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close(context.WithoutCancel(t.Context()))) }()

	_, err = conn.Exec(t.Context(), "ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH ALLOW_CONNECTIONS false")
	require.NoError(t, err)

	_, err = conn.Exec(t.Context(), "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()", name)
	require.NoError(t, err)
}

// migrateDatabase applies every migration to the database at dbURL, as an
// operator does with `sidekick migrate up`.
func migrateDatabase(t *testing.T, dbURL string) {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), dbURL)
	require.NoError(t, err)

	defer pool.Close()

	require.NoError(t, store.Migrate(t.Context(), pool))
}

//go:build integration

package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// testJobKind is the kind of the jobs the tests register.
	testJobKind = "test_job"
	// jobTimeout bounds how long a test waits for a job to run.
	jobTimeout = 30 * time.Second
)

func TestNewStartUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// url returns the database URL the config names.
		url  func(t testing.TB) string
		mode config.MigrationMode
		// config changes the written config before New reads it.
		config      func(content string) string
		wantErr     string
		wantErrors  bool
		wantCurrent bool
	}{
		{
			name:        "startup mode migrates an empty database",
			url:         storetest.NewEmptyURL,
			mode:        config.MigrationsStartup,
			wantCurrent: true,
		},
		{
			name: "manual mode leaves an empty database alone",
			url:  storetest.NewEmptyURL,
			mode: config.MigrationsManual,
		},
		{
			name:       "config with problems",
			url:        storetest.NewURL,
			mode:       "sometimes",
			wantErr:    `load config: load config CONFIG: database.migrations: expected startup or manual, got "sometimes"`,
			wantErrors: true,
		},
		{
			name: "database that cannot be reached",
			url: func(testing.TB) string {
				return "postgres://sidekick:sidekick@127.0.0.1:1/sidekick?sslmode=disable&connect_timeout=5"
			},
			mode:    config.MigrationsStartup,
			wantErr: "open database: connect: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := tt.url(t)
			configPath := writeConfig(t, dbURL, tt.mode)

			a, err := New(t.Context(), Options{ConfigPath: configPath, Registries: fakeRegistries(), LogOutput: io.Discard})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, strings.ReplaceAll(tt.wantErr, "CONFIG", configPath))

				var problems config.Errors
				require.Equal(t, tt.wantErrors, errors.As(err, &problems))

				return
			}

			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, a.Close()) })

			current, target, err := store.SchemaVersion(t.Context(), a.Pool())
			require.NoError(t, err)
			require.Equal(t, tt.wantCurrent, current == target, "schema at %d of %d", current, target)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "registered jobs run, with queue metrics",
			check: func(t *testing.T) {
				t.Helper()

				ran := make(chan db.Job, 1)

				r := startApp(t, storetest.NewURL(t), config.MigrationsStartup, func(a *App) {
					require.NoError(t, a.RegisterJob(testJobKind, queue.HandlerFunc(func(_ context.Context, job db.Job) error {
						ran <- job

						return nil
					}), queue.KindOptions{}))
				})

				id := enqueue(t, r.app.Pool())

				select {
				case job := <-ran:
					require.Equal(t, id, job.ID)
				case <-time.After(jobTimeout):
					t.Fatal("the job did not run")
				}

				_, body := get(t, r.metrics+"/metrics")
				require.Contains(t, body, `sidekick_queue_wait_seconds_count{kind="test_job"} 1`)
			},
		},
		{
			name: "registered routes are served on the main listener",
			check: func(t *testing.T) {
				t.Helper()

				r := startApp(t, storetest.NewURL(t), config.MigrationsStartup, func(a *App) {
					require.NoError(t, a.RegisterRoutes(func(mux *http.ServeMux) {
						mux.HandleFunc("GET /hello", func(w http.ResponseWriter, _ *http.Request) {
							writeText(w, http.StatusOK, "hello\n")
						})
					}))
				})

				code, body := get(t, r.main+"/hello")
				require.Equal(t, http.StatusOK, code)
				require.Equal(t, "hello\n", body)
			},
		},
		{
			name: "nothing can be registered once running",
			check: func(t *testing.T) {
				t.Helper()

				r := startApp(t, storetest.NewURL(t), config.MigrationsStartup, nil)

				require.ErrorIs(t, r.app.RegisterRoutes(func(*http.ServeMux) {}), errAlreadyRunning)
				require.ErrorIs(t, r.app.RegisterJob(testJobKind, queue.HandlerFunc(func(context.Context, db.Job) error { return nil }), queue.KindOptions{}), errAlreadyRunning)
				require.ErrorIs(t, r.app.Run(t.Context()), errAlreadyRunning)
			},
		},
		{
			name: "start-up is logged as JSON with the component",
			check: func(t *testing.T) {
				t.Helper()

				r := startApp(t, storetest.NewURL(t), config.MigrationsStartup, nil)

				require.Contains(t, r.logs.String(), `"msg":"sidekick started","component":"app"`)
				require.Contains(t, r.logs.String(), `"msg":"not starting the job queue: no job handlers are registered"`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t)
		})
	}
}

func TestShutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// blockJob registers a handler that runs until its context ends.
		blockJob bool
		// wantStatus is the status of the blocked job after shutdown.
		wantStatus string
	}{
		{name: "idle"},
		{name: "a job that does not finish is handed back", blockJob: true, wantStatus: queue.StatusPending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			running := make(chan struct{})
			dbURL := storetest.NewURL(t)

			r := startApp(t, dbURL, config.MigrationsStartup, func(a *App) {
				if !tt.blockJob {
					return
				}

				require.NoError(t, a.RegisterJob(testJobKind, queue.HandlerFunc(func(ctx context.Context, _ db.Job) error {
					close(running)
					<-ctx.Done()

					return context.Cause(ctx)
				}), queue.KindOptions{}))
			})

			var id int64

			if tt.blockJob {
				id = enqueue(t, r.app.Pool())

				select {
				case <-running:
				case <-time.After(jobTimeout):
					t.Fatal("the job did not start")
				}
			}

			start := time.Now()

			require.NoError(t, r.stop())
			require.Less(t, time.Since(start), ShutdownGrace)
			require.Contains(t, r.logs.String(), `"msg":"sidekick stopped"`)

			if !tt.blockJob {
				return
			}

			// The app has closed its pool, so the job is read through one of
			// the test's own.
			pool, err := pgxpool.New(t.Context(), dbURL)
			require.NoError(t, err)
			t.Cleanup(pool.Close)

			job, err := db.New(pool).GetJob(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, job.Status)
		})
	}
}

// enqueue adds a test job and returns its ID.
func enqueue(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()

	id, err := queue.Enqueue(t.Context(), pool, queue.NewJob{Kind: testJobKind, Payload: []byte(`{}`)})
	require.NoError(t, err)

	return id
}

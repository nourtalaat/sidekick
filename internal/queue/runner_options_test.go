package queue

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// unusedURL is a database URL for tests that never connect: a pool does not
// connect until it is used.
const unusedURL = "postgres://sidekick@localhost:1/unused"

func TestRegister(t *testing.T) {
	t.Parallel()

	noop := HandlerFunc(func(context.Context, db.Job) error { return nil })

	tests := []struct {
		name     string
		limits   Limits
		kind     string
		handler  Handler
		opts     KindOptions
		wantOpts KindOptions
		wantErr  string
	}{
		{
			name:     "short job defaults",
			kind:     KindPublish,
			handler:  noop,
			wantOpts: KindOptions{Concurrency: DefaultConcurrency, Lease: DefaultShortLease},
		},
		{
			name:    "long job defaults",
			kind:    KindBenchGate,
			handler: noop,
			opts:    KindOptions{Long: true},
			wantOpts: KindOptions{
				Concurrency: DefaultConcurrency, Long: true, Lease: DefaultLongLease, HeartbeatInterval: DefaultHeartbeatInterval,
			},
		},
		{
			name:    "options kept",
			limits:  Limits{Global: 8},
			kind:    KindInvestigate,
			handler: noop,
			opts:    KindOptions{Concurrency: 4, Long: true, Lease: time.Minute, HeartbeatInterval: 10 * time.Second},
			wantOpts: KindOptions{
				Concurrency: 4, Long: true, Lease: time.Minute, HeartbeatInterval: 10 * time.Second,
			},
		},
		{name: "no kind", handler: noop, wantErr: "kind and handler are required"},
		{name: "no handler", kind: KindPublish, wantErr: "kind and handler are required"},
		{
			name:    "investigations need limits",
			kind:    KindFollowUp,
			handler: noop,
			wantErr: "Options.Limits must be set for investigation kinds",
		},
		{
			name:    "too much concurrency",
			kind:    KindPublish,
			handler: noop,
			opts:    KindOptions{Concurrency: maxConcurrency + 1},
			wantErr: "concurrency is 1025",
		},
		{
			name:    "heartbeat as long as the lease",
			kind:    KindBenchGate,
			handler: noop,
			opts:    KindOptions{Long: true, Lease: time.Minute, HeartbeatInterval: time.Minute},
			wantErr: "heartbeat interval 1m0s must be shorter than the lease 1m0s",
		},
		{
			name:    "negative lease",
			kind:    KindPublish,
			handler: noop,
			opts:    KindOptions{Lease: -time.Second},
			wantErr: "must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner, err := NewRunner(unusedPool(t), Options{Limits: tt.limits})
			require.NoError(t, err)

			err = runner.Register(tt.kind, tt.handler, tt.opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			w := runner.workerFor(tt.kind)
			require.Equal(t, tt.wantOpts, w.opts)
			require.Equal(t, tt.wantOpts.Concurrency, cap(w.slots))

			require.ErrorContains(t, runner.Register(tt.kind, tt.handler, tt.opts), "kind already has a handler")
		})
	}
}

func TestNewRunner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		want    Options
		wantErr string
	}{
		{
			name: "defaults",
			opts: Options{WorkerID: "replica-1"},
			want: Options{
				WorkerID: "replica-1", PollInterval: DefaultPollInterval, ReapInterval: DefaultReapInterval,
				ShutdownTimeout: DefaultShutdownTimeout,
			},
		},
		{name: "bad limits", opts: Options{Limits: Limits{Routes: map[string]int{"a": 1}}}, wantErr: "global limit is 0"},
		{name: "negative interval", opts: Options{PollInterval: -time.Second}, wantErr: "must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner, err := NewRunner(unusedPool(t), tt.opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, runner.opts)
		})
	}
}

func TestRunNeedsAHandler(t *testing.T) {
	t.Parallel()

	runner, err := NewRunner(unusedPool(t), Options{})
	require.NoError(t, err)
	require.ErrorContains(t, runner.Run(t.Context()), "no handlers are registered")
}

func TestDefaultWorkerIDsDiffer(t *testing.T) {
	t.Parallel()

	first, second := defaultWorkerID(), defaultWorkerID()
	require.NotEqual(t, first, second)
}

func unusedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), unusedURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

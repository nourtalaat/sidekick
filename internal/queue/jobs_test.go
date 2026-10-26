//go:build integration

package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// clockSlack is how far apart two timestamps taken by the database during
// one test step may be.
const clockSlack = 2 * time.Second

func TestEnqueueStoresTheJob(t *testing.T) {
	t.Parallel()

	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	later := time.Now().Add(time.Hour).Truncate(time.Microsecond)

	tests := []struct {
		name            string
		ctx             func(context.Context) context.Context
		job             NewJob
		wantRoute       *string
		wantKey         *string
		wantPriority    int16
		wantMaxAttempts int32
		wantRunAt       *time.Time
		wantTraceparent *string
	}{
		{
			name:            "defaults",
			job:             NewJob{Kind: KindPublish, Payload: []byte(`{"sink":"slack"}`)},
			wantPriority:    DefaultPriority,
			wantMaxAttempts: DefaultMaxAttempts,
		},
		{
			name: "every field set",
			ctx: func(ctx context.Context) context.Context {
				return trace.ContextWithSpanContext(ctx, spanContext)
			},
			job: NewJob{
				Kind:        KindInvestigate,
				Route:       "payments",
				Payload:     []byte(`{"group":"g1"}`),
				UniqueKey:   "investigate:g1",
				Priority:    10,
				RunAt:       later,
				MaxAttempts: 2,
			},
			wantRoute:       new("payments"),
			wantKey:         new("investigate:g1"),
			wantPriority:    10,
			wantMaxAttempts: 2,
			wantRunAt:       &later,
			wantTraceparent: new("00-0102030405060708090a0b0c0d0e0f10-0102030405060708-01"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			ctx := t.Context()
			if tt.ctx != nil {
				ctx = tt.ctx(ctx)
			}

			id, err := Enqueue(ctx, pool, tt.job)
			require.NoError(t, err)

			job := getJob(t, pool, id)
			require.Equal(t, tt.job.Kind, job.Kind)
			require.JSONEq(t, string(tt.job.Payload), string(job.Payload))
			require.Equal(t, StatusPending, job.Status)
			require.Equal(t, tt.wantRoute, job.Route)
			require.Equal(t, tt.wantKey, job.UniqueKey)
			require.Equal(t, tt.wantPriority, job.Priority)
			require.Equal(t, tt.wantMaxAttempts, job.MaxAttempts)
			require.Equal(t, tt.wantTraceparent, job.Traceparent)
			require.Zero(t, job.Attempts)
			require.Nil(t, job.ClaimToken)

			if tt.wantRunAt != nil {
				require.True(t, tt.wantRunAt.Equal(job.RunAt), "run_at %s, want %s", job.RunAt, tt.wantRunAt)
			} else {
				require.WithinDuration(t, job.CreatedAt, job.RunAt, clockSlack)
			}
		})
	}
}

func TestEnqueueWithAnActiveUniqueKey(t *testing.T) {
	t.Parallel()

	const key = "investigate:g1"

	tests := []struct {
		name string
		// prepare leaves the first job with the key in some state.
		prepare func(t *testing.T, pool *pgxpool.Pool, id int64)
		wantErr error
	}{
		{name: "pending job", prepare: func(*testing.T, *pgxpool.Pool, int64) {}, wantErr: ErrDuplicate},
		{
			name: "running job",
			prepare: func(t *testing.T, pool *pgxpool.Pool, _ int64) {
				mustClaimOne(t, pool, KindInvestigate)
			},
			wantErr: ErrDuplicate,
		},
		{
			name: "done job",
			prepare: func(t *testing.T, pool *pgxpool.Pool, _ int64) {
				require.NoError(t, complete(t.Context(), pool, mustClaimOne(t, pool, KindInvestigate)))
			},
		},
		{
			name: "dead job",
			prepare: func(t *testing.T, pool *pgxpool.Pool, _ int64) {
				_, err := fail(t.Context(), pool, mustClaimOne(t, pool, KindInvestigate), errors.New("boom"), true, 0)
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			first := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, UniqueKey: key})
			tt.prepare(t, pool, first)

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			_, err = Enqueue(t.Context(), tx, NewJob{Kind: KindInvestigate, UniqueKey: key, Payload: []byte(`{}`)})
			require.ErrorIs(t, err, tt.wantErr)

			// The transaction is still usable after a duplicate.
			_, err = Enqueue(t.Context(), tx, NewJob{Kind: KindPublish, Payload: []byte(`{}`)})
			require.NoError(t, err)
			require.NoError(t, tx.Commit(t.Context()))
		})
	}
}

func TestEnqueueNotifiesWhenTheTransactionCommits(t *testing.T) {
	t.Parallel()

	const wait = 200 * time.Millisecond

	pool := setup(t)

	listener, err := pool.Acquire(t.Context())
	require.NoError(t, err)
	t.Cleanup(listener.Release)
	require.NoError(t, db.New(listener).ListenJobsReady(t.Context()))

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)

	_, err = Enqueue(t.Context(), tx, NewJob{Kind: KindRouteEvent, Payload: []byte(`{}`)})
	require.NoError(t, err)

	early, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()

	_, err = listener.Conn().WaitForNotification(early)
	require.ErrorIs(t, err, context.DeadlineExceeded, "no notification before the commit")

	require.NoError(t, tx.Commit(t.Context()))

	notification, err := listener.Conn().WaitForNotification(t.Context())
	require.NoError(t, err)
	require.Equal(t, "jobs_ready", notification.Channel)
	require.Equal(t, KindRouteEvent, notification.Payload)
}

func TestClaimTakesReadyJobsInOrder(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	past := time.Now().Add(-time.Hour)

	low := mustEnqueue(t, pool, NewJob{Kind: KindPublish, Priority: 200, RunAt: past})
	newer := mustEnqueue(t, pool, NewJob{Kind: KindPublish})
	older := mustEnqueue(t, pool, NewJob{Kind: KindPublish, RunAt: past})
	urgent := mustEnqueue(t, pool, NewJob{Kind: KindPublish, Priority: 50})
	mustEnqueue(t, pool, NewJob{Kind: KindPublish, RunAt: time.Now().Add(time.Hour)})
	mustEnqueue(t, pool, NewJob{Kind: KindRouteEvent})

	var lastToken int64

	for _, want := range []int64{urgent, older, newer, low} {
		job := mustClaimOne(t, pool, KindPublish)
		require.Equal(t, want, job.ID)
		require.Equal(t, StatusRunning, job.Status)
		require.Equal(t, new(testWorker), job.LockedBy)
		require.Zero(t, job.Attempts, "claiming does not count an attempt")
		require.NotNil(t, job.ClaimToken)
		require.Greater(t, *job.ClaimToken, lastToken, "every claim takes a higher token")
		require.NotNil(t, job.LockedUntil)
		require.WithinDuration(t, time.Now().Add(testLease), *job.LockedUntil, clockSlack)

		lastToken = *job.ClaimToken
	}

	const batch = 10

	again, err := claim(t.Context(), pool, testWorker, KindPublish, testLease, batch)
	require.NoError(t, err)
	require.Empty(t, again, "the job in the future and the other kind are left")
}

func TestClaimTakesABatchWithDistinctTokens(t *testing.T) {
	t.Parallel()

	const (
		jobs  = 5
		batch = 3
	)

	pool := setup(t)

	for range jobs {
		mustEnqueue(t, pool, NewJob{Kind: KindPublish})
	}

	first, err := claim(t.Context(), pool, testWorker, KindPublish, testLease, batch)
	require.NoError(t, err)
	require.Len(t, first, batch)

	rest, err := claim(t.Context(), pool, testWorker, KindPublish, testLease, batch)
	require.NoError(t, err)
	require.Len(t, rest, jobs-batch)

	tokens := map[int64]bool{}
	for _, job := range append(first, rest...) {
		tokens[*job.ClaimToken] = true
	}

	require.Len(t, tokens, jobs)
}

func TestWritesToAClaimedJobCheckTheToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		write      func(ctx context.Context, pool *pgxpool.Pool, job db.Job) error
		wantStatus string
	}{
		{name: "complete", write: func(ctx context.Context, pool *pgxpool.Pool, job db.Job) error {
			return complete(ctx, pool, job)
		}, wantStatus: StatusDone},
		{name: "fail", write: func(ctx context.Context, pool *pgxpool.Pool, job db.Job) error {
			_, err := fail(ctx, pool, job, errors.New("boom"), false, time.Second)

			return err
		}, wantStatus: StatusPending},
		{name: "hand back", write: func(ctx context.Context, pool *pgxpool.Pool, job db.Job) error {
			return handBack(ctx, pool, job)
		}, wantStatus: StatusPending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			id := mustEnqueue(t, pool, NewJob{Kind: KindPublish})
			job := mustClaimOne(t, pool, KindPublish)

			stale := job
			stale.ClaimToken = new(*job.ClaimToken - 1)
			require.ErrorIs(t, tt.write(t.Context(), pool, stale), ErrLeaseLost)
			require.Equal(t, StatusRunning, getJob(t, pool, id).Status)

			require.NoError(t, tt.write(t.Context(), pool, job))
			require.Equal(t, tt.wantStatus, getJob(t, pool, id).Status)

			require.ErrorIs(t, tt.write(t.Context(), pool, job), ErrLeaseLost, "a second write finds the job no longer running")
		})
	}
}

func TestFail(t *testing.T) {
	t.Parallel()

	const backoff = 30 * time.Second

	tests := []struct {
		name         string
		maxAttempts  int32
		giveUp       bool
		wantStatus   string
		wantAttempts int32
		wantDelay    time.Duration
	}{
		{name: "retried after the backoff", maxAttempts: 3, wantStatus: StatusPending, wantAttempts: 1, wantDelay: backoff},
		{name: "last attempt makes it dead", maxAttempts: 1, wantStatus: StatusDead, wantAttempts: 1},
		{name: "giving up makes it dead at once", maxAttempts: 3, giveUp: true, wantStatus: StatusDead, wantAttempts: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			id := mustEnqueue(t, pool, NewJob{Kind: KindPublish, MaxAttempts: tt.maxAttempts})
			job := mustClaimOne(t, pool, KindPublish)

			failed, err := fail(t.Context(), pool, job, errors.New("slack returned 500"), tt.giveUp, backoff)
			require.NoError(t, err)
			require.Equal(t, getJob(t, pool, id), failed)
			require.Equal(t, tt.wantStatus, failed.Status)
			require.Equal(t, tt.wantAttempts, failed.Attempts)
			require.Equal(t, new("slack returned 500"), failed.LastError)
			require.Nil(t, failed.LockedBy)
			require.Nil(t, failed.LockedUntil)

			if tt.wantStatus == StatusDead {
				require.NotNil(t, failed.FinishedAt)

				return
			}

			require.Nil(t, failed.FinishedAt)
			require.InDelta(t, tt.wantDelay.Seconds(), secondsUntilRunAt(t, pool, id), clockSlack.Seconds())
		})
	}
}

func TestDeadJobCanBeRetried(t *testing.T) {
	t.Parallel()

	const maxAttempts = 3

	pool := setup(t)
	id := mustEnqueue(t, pool, NewJob{Kind: KindPublish, MaxAttempts: maxAttempts, UniqueKey: "publish:1"})

	for attempt := range maxAttempts {
		require.ErrorIs(t, RetryDead(t.Context(), pool, id), ErrNotDead, "attempt %d", attempt)

		makeReady(t, pool, id)

		failed, err := fail(t.Context(), pool, mustClaimOne(t, pool, KindPublish), errors.New("boom"), false, time.Hour)
		require.NoError(t, err)
		require.Equal(t, int32(attempt+1), failed.Attempts)
	}

	dead := getJob(t, pool, id)
	require.Equal(t, StatusDead, dead.Status)
	require.NotNil(t, dead.FinishedAt)

	require.NoError(t, RetryDead(t.Context(), pool, id))

	retried := getJob(t, pool, id)
	require.Equal(t, StatusPending, retried.Status)
	require.Zero(t, retried.Attempts)
	require.Nil(t, retried.FinishedAt)

	job := mustClaimOne(t, pool, KindPublish)
	require.Equal(t, id, job.ID)
	require.Greater(t, *job.ClaimToken, *dead.ClaimToken)

	require.ErrorIs(t, RetryDead(t.Context(), pool, id), ErrNotDead, "a running job is not dead")
	require.ErrorIs(t, RetryDead(t.Context(), pool, id+1), ErrNotDead, "no such job")
}

func TestRetryDeadWithAnActiveUniqueKey(t *testing.T) {
	t.Parallel()

	const key = "investigate:g1"

	pool := setup(t)
	id := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, UniqueKey: key})
	_, err := fail(t.Context(), pool, mustClaimOne(t, pool, KindInvestigate), errors.New("boom"), true, 0)
	require.NoError(t, err)

	mustEnqueue(t, pool, NewJob{Kind: KindInvestigate, UniqueKey: key})

	require.ErrorIs(t, RetryDead(t.Context(), pool, id), ErrDuplicate)
	require.Equal(t, StatusDead, getJob(t, pool, id).Status)
}

func TestHandBackKeepsAttempts(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	id := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate})

	_, err := fail(t.Context(), pool, mustClaimOne(t, pool, KindInvestigate), errors.New("boom"), false, 0)
	require.NoError(t, err)

	job := mustClaimOne(t, pool, KindInvestigate)
	require.NoError(t, handBack(t.Context(), pool, job))

	handedBack := getJob(t, pool, id)
	require.Equal(t, StatusPending, handedBack.Status)
	require.Equal(t, int32(1), handedBack.Attempts)
	require.Nil(t, handedBack.LockedBy)
	require.Nil(t, handedBack.LockedUntil)
	require.InDelta(t, 0, secondsUntilRunAt(t, pool, id), clockSlack.Seconds())

	next := mustClaimOne(t, pool, KindInvestigate)
	require.Greater(t, *next.ClaimToken, *job.ClaimToken, "the next claim gets a new token")
}

// secondsUntilRunAt returns how far the job's run_at is from the database's
// clock now.
func secondsUntilRunAt(t *testing.T, pool *pgxpool.Pool, id int64) float64 {
	t.Helper()

	var seconds float64

	err := pool.QueryRow(t.Context(), "SELECT extract(epoch FROM run_at - now())::float8 FROM jobs WHERE id = $1", id).Scan(&seconds)
	require.NoError(t, err)

	return seconds
}

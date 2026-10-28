// Package queue runs Sidekick's background jobs from the jobs table in
// Postgres.
//
// Callers add jobs with Enqueue, usually in the same transaction as the row
// the job is about. A Runner claims ready jobs with a lease, runs the
// handler registered for each kind, and completes, retries or hands back
// each job. Every claim takes a new claim token from job_claim_seq, and every
// write to a claimed job checks it, so a worker that lost its lease cannot
// complete or fail a job that another worker now owns. Handlers see the
// token and use it to fence their own writes.
//
// Every coordination between replicas goes through Postgres: row locks with
// SKIP LOCKED for claims, an advisory lock for the reaper, and a
// transaction-level advisory lock for the investigation concurrency limits.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/propagation"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The job kinds that Sidekick runs.
const (
	KindRouteEvent      = "route_event"
	KindInvestigate     = "investigate"
	KindFollowUp        = "follow_up"
	KindPublish         = "publish"
	KindExecuteAction   = "execute_action"
	KindDraftLesson     = "draft_lesson"
	KindProposeStrategy = "propose_strategy"
	KindBenchGate       = "bench_gate"
	KindCloseGroup      = "close_group"
	KindWatchTick       = "watch_tick"
	KindRollup          = "rollup"
	KindRetention       = "retention"
	KindNotice          = "notice"
	KindEmbedMemory     = "embed_memory"
)

// The values of jobs.status.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusDead    = "dead"
)

// Defaults for the fields of NewJob that are left at zero.
const (
	DefaultPriority    int16 = 100
	DefaultMaxAttempts int32 = 5
)

const (
	// traceparentHeader is the W3C Trace Context field that jobs store.
	traceparentHeader = "traceparent"
	// uniqueViolation is the Postgres error code unique_violation.
	uniqueViolation = "23505"
)

var (
	// ErrDuplicate means a pending or running job already has the job's
	// unique key, so nothing was added. Enqueue returns it without a
	// database error, so the caller's transaction can go on.
	ErrDuplicate = errors.New("a job with this unique key is already pending or running")

	// ErrGiveUp, wrapped into a handler's error, marks the job dead at once
	// instead of retrying it.
	ErrGiveUp = errors.New("give up on the job")

	// ErrLeaseLost means the job no longer belongs to the claim token: its
	// lease expired, and the reaper or another worker took it over.
	ErrLeaseLost = errors.New("the job's lease was lost")

	// ErrNotDead means RetryDead found no dead job with that ID.
	ErrNotDead = errors.New("no dead job with this ID")
)

// NewJob is a job to add with Enqueue. Fields left at zero take their
// defaults.
type NewJob struct {
	// Kind picks the handler, for example KindInvestigate.
	Kind string
	// Route is the route the job belongs to, for the per-route concurrency
	// limits of investigation kinds. Empty means none.
	Route string
	// Payload is the handler's input. It must be valid JSON.
	Payload json.RawMessage
	// UniqueKey, when set, allows only one pending or running job with this
	// key, for example "investigate:<group id>".
	UniqueKey string
	// Priority orders ready jobs: lower numbers are claimed first. Zero means
	// DefaultPriority.
	Priority int16
	// RunAt delays the job until then. Zero means now.
	RunAt time.Time
	// MaxAttempts is how many failures make the job dead. Zero means
	// DefaultMaxAttempts.
	MaxAttempts int32
}

// Enqueue adds a pending job and wakes the workers, and returns the job's
// ID. Pass a transaction as q to add the job together with the caller's own
// writes; the workers wake when it commits. The traceparent of the span in
// ctx, if any, is stored with the job.
//
// When a pending or running job has the same UniqueKey, Enqueue adds nothing
// and returns ErrDuplicate.
func Enqueue(ctx context.Context, q db.DBTX, job NewJob) (int64, error) {
	if job.Kind == "" {
		return 0, errors.New("enqueue job: kind is empty")
	}

	if !json.Valid(job.Payload) {
		return 0, fmt.Errorf("enqueue %s job: payload is not valid JSON", job.Kind)
	}

	params := db.EnqueueJobParams{
		Kind:        job.Kind,
		Route:       optional(job.Route),
		Payload:     job.Payload,
		UniqueKey:   optional(job.UniqueKey),
		Priority:    orDefault(job.Priority, DefaultPriority),
		MaxAttempts: orDefault(job.MaxAttempts, DefaultMaxAttempts),
		Traceparent: optional(traceparent(ctx)),
	}

	if !job.RunAt.IsZero() {
		params.RunAt = &job.RunAt
	}

	queries := db.New(q)

	id, err := queries.EnqueueJob(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrDuplicate
	}

	if err != nil {
		return 0, fmt.Errorf("enqueue %s job: %w", job.Kind, err)
	}

	if err := queries.NotifyJobsReady(ctx, job.Kind); err != nil {
		return 0, fmt.Errorf("notify workers of %s job %d: %w", job.Kind, id, err)
	}

	return id, nil
}

// RetryDead sets a dead job back to pending, with its attempts reset to
// zero, and wakes the workers. It returns ErrNotDead when there is no dead
// job with that ID, and ErrDuplicate when another pending or running job has
// the same unique key. The duplicate case is a database error, so inside a
// transaction it aborts the transaction.
func RetryDead(ctx context.Context, q db.DBTX, id int64) error {
	queries := db.New(q)

	kind, err := queries.RetryDeadJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotDead
	}

	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolation {
			return ErrDuplicate
		}

		return fmt.Errorf("retry dead job %d: %w", id, err)
	}

	if err := queries.NotifyJobsReady(ctx, kind); err != nil {
		return fmt.Errorf("notify workers of retried job %d: %w", id, err)
	}

	return nil
}

// ReleaseUniqueKey clears the unique key of job, which the caller is
// running, so that Enqueue can add a new job with the same key before this
// one finishes. The job stays running and is completed as usual when its
// handler returns. A handler calls it once it will do no more work that
// the key guards, usually in the transaction that records the last of that
// work, so that a job enqueued after the commit does not miss anything.
//
// It returns ErrLeaseLost when the job no longer belongs to job's claim
// token.
func ReleaseUniqueKey(ctx context.Context, q db.DBTX, job db.Job) error {
	rows, err := db.New(q).ReleaseUniqueKey(ctx, db.ReleaseUniqueKeyParams{ID: job.ID, ClaimToken: ClaimToken(job)})
	if err != nil {
		return fmt.Errorf("release the unique key of job %d: %w", job.ID, err)
	}

	if rows == 0 {
		return ErrLeaseLost
	}

	return nil
}

// ClaimToken returns the claim token of a claimed job, the value a handler
// fences its own writes with. Claims always set it; a job that was never
// claimed gets 0, which matches no claim.
func ClaimToken(job db.Job) int64 {
	if job.ClaimToken == nil {
		return 0
	}

	return *job.ClaimToken
}

// TraceContext returns ctx with the span that enqueued job as the remote
// parent, when the job carries the traceparent Enqueue stored, so the
// handler's span joins the trace of the request that queued the job. A job
// without one returns ctx unchanged.
func TraceContext(ctx context.Context, job db.Job) context.Context {
	if job.Traceparent == nil {
		return ctx
	}

	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{traceparentHeader: *job.Traceparent})
}

// traceparent returns the W3C traceparent of the span in ctx, or "" when ctx
// holds no valid span.
func traceparent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	return carrier.Get(traceparentHeader)
}

// optional turns "" into NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

func orDefault[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}

	return value
}

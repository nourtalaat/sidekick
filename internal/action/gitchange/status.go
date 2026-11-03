package gitchange

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// statusSpanName is the name of the span of one run of the status job.
const statusSpanName = "gitchange.check_pull_requests"

// tracerName names the tracer of the status job.
const tracerName = "github.com/nourtalaat/sidekick/internal/action/gitchange"

// The results the job counts besides the states of a request.
const (
	resultMissing = "missing"
	resultError   = "error"
)

// StatusOptions set up a StatusChecker. Pool and Actions are required.
type StatusOptions struct {
	Pool *pgxpool.Pool
	// Actions are the configured actions, by name. The ones that implement
	// StateReporter are the ones it checks; the others are left alone.
	Actions map[string]action.Action
	// Logger gets a line per pull request that could not be checked, with
	// IDs only. Nil logs nothing.
	Logger *slog.Logger
	// TracerProvider makes the tracer of the job's spans. Nil means the
	// global tracer provider.
	TracerProvider trace.TracerProvider
	// Now is the clock the tracking window is measured by. Nil means
	// time.Now.
	Now func() time.Time
}

// StatusChecker runs the daily job that reads from the Git host whether the
// pull requests of succeeded proposals are still open, were merged or were
// closed, and records it in pull_request_states. Make it with
// NewStatusChecker and register it for queue.KindCheckPullRequests.
type StatusChecker struct {
	pool      *pgxpool.Pool
	reporters map[string]StateReporter
	names     []string
	log       *slog.Logger
	tracer    trace.Tracer
	now       func() time.Time
}

// NewStatusChecker returns a StatusChecker for the actions among
// opts.Actions that implement StateReporter.
func NewStatusChecker(opts StatusOptions) *StatusChecker {
	c := &StatusChecker{pool: opts.Pool, reporters: map[string]StateReporter{}, log: opts.Logger, now: opts.Now}

	for name, a := range opts.Actions {
		if reporter, ok := a.(StateReporter); ok {
			c.reporters[name] = reporter
			c.names = append(c.names, name)
		}
	}

	slices.Sort(c.names)

	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}

	if c.now == nil {
		c.now = time.Now
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	c.tracer = tp.Tracer(tracerName)

	return c
}

// Enabled reports whether any configured action can report the state of its
// pull requests. A deployment with none has nothing to check.
func (c *StatusChecker) Enabled() bool {
	return len(c.reporters) > 0
}

// Handle runs one check_pull_requests job: it reads the pull request of up
// to MaxChecksPerRun proposals that have none recorded or last showed
// open, and records what the host says. A pull request that cannot be read
// for good, for example in a repository the credential lost access to, is
// logged and skipped. Rate limits and other temporary failures end the job
// with an error so it is tried again later. Repeating the job is safe: a
// recorded merge or close never changes.
func (c *StatusChecker) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := c.tracer.Start(queue.TraceContext(ctx, job), statusSpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "the pull requests were not all checked")
		}

		span.End()
	}()

	now := c.now()

	rows, err := db.New(c.pool).ListPullRequestsToCheck(ctx, db.ListPullRequestsToCheckParams{
		Actions: c.names, Since: now.Add(-TrackedFor), Before: now, Batch: MaxChecksPerRun,
	})
	if err != nil {
		return fmt.Errorf("list the pull requests to check: %w", err)
	}

	span.SetAttributes(attribute.Int("sidekick.pull_requests.to_check", len(rows)))

	var (
		failed int
		first  error
	)

	for _, row := range rows {
		if err := c.check(ctx, job, row, now); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("check pull requests: %w", err)
			}

			c.reporters[row.Action].CountCheck(resultError)
			c.log.WarnContext(ctx, "could not check a pull request", "proposal_id", row.ID.String(), "action", row.Action)

			if !errors.Is(err, action.ErrPermanent) {
				failed++

				if first == nil {
					first = err
				}
			}
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d pull requests could not be checked, the first: %w", failed, len(rows), first)
	}

	return nil
}

// check reads one pull request and records its state.
func (c *StatusChecker) check(ctx context.Context, job db.Job, row db.ListPullRequestsToCheckRow, now time.Time) error {
	reporter := c.reporters[row.Action]

	repo, st, err := reporter.PullRequestState(ctx, row.Params, action.IdempotencyKey(row.ID.String()))
	if err != nil {
		return fmt.Errorf("read the state of proposal %s: %w", row.ID, err)
	}

	if st == nil {
		reporter.CountCheck(resultMissing)

		return nil
	}

	_, err = db.New(c.pool).UpsertPullRequestState(ctx, db.UpsertPullRequestStateParams{
		ProposalID: row.ID,
		Repo:       repo,
		Number:     int64(st.Number),
		Url:        st.URL,
		State:      st.State,
		MergedAt:   st.MergedAt,
		ClosedAt:   st.ClosedAt,
		Now:        now,
		JobID:      job.ID,
		ClaimToken: queue.ClaimToken(job),
	})
	if err != nil {
		return fmt.Errorf("record the state of proposal %s: %w", row.ID, err)
	}

	reporter.CountCheck(st.State)

	return nil
}

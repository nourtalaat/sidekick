package githubpr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Limits of one run of the status job.
const (
	// trackedFor is how long after its proposal succeeded a pull request is
	// still checked. One that stays open longer is given up on.
	trackedFor = 90 * 24 * time.Hour
	// maxChecksPerRun is the most pull requests one run reads from GitHub.
	// The rest wait for the next run.
	maxChecksPerRun = 1000
)

// statusSpanName is the name of the span of one run of the status job.
const statusSpanName = "githubpr.check_pull_requests"

// Status is what GitHub says about the pull request of a proposal.
type Status struct {
	Number int
	URL    string
	State  string
	// MergedAt is set for a merged pull request, ClosedAt for one closed
	// without a merge.
	MergedAt *time.Time
	ClosedAt *time.Time
}

// status reads the pull request made from branch, in any state. It returns
// nil when there is none.
func (p *PR) status(ctx context.Context, owner, name, branch string) (*Status, error) {
	pr, err := p.pullRequestFrom(ctx, owner, name, branch)
	if err != nil {
		return nil, fmt.Errorf("read the pull request: %w", err)
	}

	if pr == nil {
		return nil, nil
	}

	s := &Status{Number: pr.GetNumber(), URL: pr.GetHTMLURL(), State: stateOf(pr)}

	switch s.State {
	case StateMerged:
		s.MergedAt = timeOf(pr.MergedAt)
	case StateClosed:
		s.ClosedAt = timeOf(pr.ClosedAt)
	}

	return s, nil
}

// timeOf returns the time of a GitHub timestamp, or nil.
func timeOf(t *gh.Timestamp) *time.Time {
	if t == nil {
		return nil
	}

	return &t.Time
}

// StatusOptions set up a StatusChecker. Pool and Actions are required.
type StatusOptions struct {
	Pool *pgxpool.Pool
	// Actions are the configured actions, by name. The github.pull_request
	// ones are the ones it checks; the others are left alone.
	Actions map[string]action.Action
	// Registerer is where the metrics are registered. Nil registers none.
	Registerer prometheus.Registerer
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

// StatusChecker runs the daily job that reads from GitHub whether the pull
// requests of succeeded proposals are still open, were merged or were
// closed, and records it in pull_request_states. Make it with
// NewStatusChecker and register it for queue.KindCheckPullRequests.
type StatusChecker struct {
	pool   *pgxpool.Pool
	prs    map[string]*PR
	names  []string
	checks *prometheus.CounterVec
	log    *slog.Logger
	tracer trace.Tracer
	now    func() time.Time
}

// NewStatusChecker returns a StatusChecker for the github.pull_request
// actions among opts.Actions.
func NewStatusChecker(opts StatusOptions) (*StatusChecker, error) {
	c := &StatusChecker{pool: opts.Pool, prs: map[string]*PR{}, log: opts.Logger, now: opts.Now}

	for name, a := range opts.Actions {
		if pr, ok := a.(*PR); ok {
			c.prs[name] = pr
			c.names = append(c.names, name)
		}
	}

	checks, err := newStatusMetric(opts.Registerer)
	if err != nil {
		return nil, fmt.Errorf("set up the status metrics: %w", err)
	}

	c.checks = checks

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

	return c, nil
}

// Enabled reports whether any github.pull_request action is configured. A
// deployment with none has nothing to check.
func (c *StatusChecker) Enabled() bool {
	return len(c.prs) > 0
}

// Handle runs one check_pull_requests job: it reads the pull request of up
// to maxChecksPerRun proposals that have none recorded or last showed
// open, and records what GitHub says. A pull request that cannot be read
// for good, for example in a repository the app lost access to, is logged
// and skipped. Rate limits and other temporary failures end the job with an
// error so it is tried again later. Repeating the job is safe: a recorded
// merge or close never changes.
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
		Actions: c.names, Since: now.Add(-trackedFor), Before: now, Batch: maxChecksPerRun,
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

			c.checks.WithLabelValues(resultError).Inc()
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
	owner, name, ok := strings.Cut(row.Repo, "/")
	if !ok || !validRepo(row.Repo) {
		return fmt.Errorf("%w: the proposal's repository %q is not owner/name", action.ErrPermanent, row.Repo)
	}

	st, err := c.prs[row.Action].status(ctx, owner, name, action.IdempotencyKey(row.ID.String()))
	if err != nil {
		return fmt.Errorf("read the state of proposal %s: %w", row.ID, err)
	}

	if st == nil {
		c.checks.WithLabelValues(resultMissing).Inc()

		return nil
	}

	_, err = db.New(c.pool).UpsertPullRequestState(ctx, db.UpsertPullRequestStateParams{
		ProposalID: row.ID,
		Repo:       row.Repo,
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

	c.checks.WithLabelValues(st.State).Inc()

	return nil
}

package gitchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// ciSpanName is the name of the span of one run of the CI report job.
const ciSpanName = "gitchange.ci_report"

// The waits between two reads of the CI: the first, and the longest the
// doubling reaches.
const (
	ciFirstWait = 30 * time.Second
	ciMaxWait   = 5 * time.Minute
)

// Metric name and labels of the CI report counter.
const (
	metricCIReports = "sidekick_ci_reports_total"
	labelHost       = "host"
	labelCIState    = "state"
)

// CIMetrics count the CI reports that reached a final state. A nil
// *CIMetrics counts nothing.
type CIMetrics struct {
	reports *prometheus.CounterVec
}

// NewCIMetrics creates sidekick_ci_reports_total and registers it with reg,
// which may be nil.
func NewCIMetrics(reg prometheus.Registerer) (*CIMetrics, error) {
	reports, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricCIReports,
		Help: "CI reports posted, by host (github, gitlab) and final state (passed, failed, none, unknown, timed_out).",
	}, []string{labelHost, labelCIState}))
	if err != nil {
		return nil, fmt.Errorf("register the CI report metric: %w", err)
	}

	return &CIMetrics{reports: reports}, nil
}

// count counts one report in its final state.
func (m *CIMetrics) count(host, state string) {
	if m == nil {
		return
	}

	m.reports.WithLabelValues(host, state).Inc()
}

// CIOptions set up a CIReporter. Pool and Actions are required.
type CIOptions struct {
	Pool *pgxpool.Pool
	// Actions are the configured actions, by name. The ones that implement
	// CIReader are the ones it reads CI for.
	Actions map[string]action.Action
	// Metrics counts the reports. Nil counts nothing.
	Metrics *CIMetrics
	// Logger gets a line when a read fails, with IDs only, never check
	// names. Nil logs nothing.
	Logger *slog.Logger
	// TracerProvider makes the tracer of the job's spans. Nil means the
	// global tracer provider.
	TracerProvider trace.TracerProvider
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// CIReporter runs the ci_report jobs. Make it with NewCIReporter and
// register it for queue.KindCIReport.
type CIReporter struct {
	pool    *pgxpool.Pool
	readers map[string]CIReader
	metrics *CIMetrics
	log     *slog.Logger
	tracer  trace.Tracer
	now     func() time.Time
}

var _ queue.Handler = (*CIReporter)(nil)

// NewCIReporter returns a CIReporter for the actions among opts.Actions that
// implement CIReader.
func NewCIReporter(opts CIOptions) *CIReporter {
	r := &CIReporter{pool: opts.Pool, readers: map[string]CIReader{}, metrics: opts.Metrics, log: opts.Logger, now: opts.Now}

	for name, a := range opts.Actions {
		if reader, ok := a.(CIReader); ok {
			r.readers[name] = reader
		}
	}

	if r.log == nil {
		r.log = slog.New(slog.DiscardHandler)
	}

	if r.now == nil {
		r.now = time.Now
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	r.tracer = tp.Tracer(tracerName)

	return r
}

// Enabled reports whether any configured action can report CI. A deployment
// with none has nothing to read.
func (r *CIReporter) Enabled() bool {
	return len(r.readers) > 0
}

// Handle runs one ci_report job: it reads the CI of the proposal's pull
// request once. When the state is final it ends the report and queues the
// thread message; otherwise it queues the next read after a longer wait. Both
// happen in one transaction that holds the report's row and gives up the
// job's unique key, so a worker that lost its lease writes nothing and of
// two workers only one reports. A read that fails for a temporary reason
// counts as not final, and the report ends as unknown at the timeout. A
// payload that does not decode, a proposal that is gone, or an action that
// is no longer configured gives up the job.
func (r *CIReporter) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := r.tracer.Start(queue.TraceContext(ctx, job), ciSpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "the CI report failed")
		}

		span.End()
	}()

	var p ciPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.ProposalID == uuid.Nil {
		return fmt.Errorf("decode the ci_report payload: %w: %w", queue.ErrGiveUp, errors.Join(err, errNoProposal))
	}

	row, err := db.New(r.pool).GetCIReportForJob(ctx, p.ProposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		r.log.DebugContext(ctx, "ci_report for a proposal that no longer exists", "proposal_id", p.ProposalID.String())

		return nil
	}

	if err != nil {
		return fmt.Errorf("load the CI report of proposal %s: %w", p.ProposalID, err)
	}

	if row.ReportedAt != nil {
		return nil
	}

	reader, ok := r.readers[row.Action]
	if !ok {
		return fmt.Errorf("proposal %s: action %q reports no CI: %w: %w", p.ProposalID, row.Action, queue.ErrGiveUp, errNoReader)
	}

	var res action.Result
	if err := json.Unmarshal(row.Result, &res); err != nil || res.PullRequest == nil {
		return fmt.Errorf("proposal %s: %w: %w", p.ProposalID, queue.ErrGiveUp, errors.Join(err, errNoPullRequest))
	}

	span.SetAttributes(attribute.String("sidekick.ci.host", reader.CIHost()))

	ci, readFailed := r.read(ctx, reader, row.Params, *res.PullRequest, p.ProposalID)
	if ctx.Err() != nil {
		return fmt.Errorf("read the CI of proposal %s: %w", p.ProposalID, context.Cause(ctx))
	}

	now := r.now()
	next := decideCI(ci, readFailed, now.Sub(row.StartedAt), reader.CIReportTimeout())

	run := ciRun{id: p.ProposalID, row: row, reader: reader, pr: *res.PullRequest, ci: ci, decision: next, now: now}
	if err := r.record(ctx, job, run); err != nil {
		return fmt.Errorf("record the CI of proposal %s: %w", p.ProposalID, err)
	}

	return nil
}

// read calls the reader. A failure that a repeat cannot fix is an unknown
// state; any other failure is reported as readFailed, with a pending state.
func (r *CIReporter) read(ctx context.Context, reader CIReader, params json.RawMessage, pr action.PullRequestRef, id uuid.UUID) (CI, bool) {
	ci, err := reader.ReadCI(ctx, params, pr)
	switch {
	case err == nil:
		return ci, false
	case errors.Is(err, action.ErrPermanent):
		r.log.WarnContext(ctx, "the CI cannot be read", "proposal_id", id.String(), "host", reader.CIHost())

		return CI{State: CIUnknown, Message: unknownMessage}, false
	default:
		r.log.WarnContext(ctx, "the CI could not be read, so it is read again", "proposal_id", id.String(), "host", reader.CIHost())

		return CI{State: CIPending}, true
	}
}

// ciDecision is what the job does with a read.
type ciDecision struct {
	// final is set when the report ends with state.
	final bool
	state string
	// waited is the time the message says Sidekick waited.
	waited time.Duration
}

// decideCI turns a read into a decision. elapsed is the time since the
// report started and timeout how long Sidekick waits. Nothing at all becomes
// "none" after the start grace, or the timeout if that is shorter. At the
// timeout, a read that failed ends as unknown; checks that had not finished
// end as failed when one failed already, and as timed out otherwise.
func decideCI(ci CI, readFailed bool, elapsed, timeout time.Duration) ciDecision {
	switch ci.State {
	case CIPassed, CIFailed, CIUnknown:
		return ciDecision{final: true, state: ci.State}
	case CINone:
		if grace := min(ciStartGrace, timeout); elapsed >= grace {
			return ciDecision{final: true, state: CINone, waited: grace}
		}
	}

	if elapsed < timeout {
		return ciDecision{state: CIPending}
	}

	switch {
	case readFailed:
		return ciDecision{final: true, state: CIUnknown}
	case ci.FailedCount > 0:
		return ciDecision{final: true, state: CIFailed}
	default:
		return ciDecision{final: true, state: CITimedOut, waited: timeout}
	}
}

// ciRun is one read of the CI of a proposal and what the job decided.
type ciRun struct {
	id       uuid.UUID
	row      db.GetCIReportForJobRow
	reader   CIReader
	pr       action.PullRequestRef
	ci       CI
	decision ciDecision
	now      time.Time
}

// record writes the decision in one transaction.
func (r *CIReporter) record(ctx context.Context, job db.Job, run ciRun) error {
	id, d, ci := run.id, run.decision, run.ci

	detail, err := json.Marshal(reportOf(run))
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}

	finished := false

	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		reportedAt, err := queries.LockCIReport(ctx, id)
		if err != nil {
			return fmt.Errorf("lock the report: %w", err)
		}

		if reportedAt != nil {
			return nil
		}

		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		if run.row.Checks == 0 && ci.Repo != "" && ci.URL != "" {
			st := &Status{Number: int(run.pr.Number), URL: ci.URL, State: StateOpen}
			if err := RecordState(ctx, tx, id.String(), ci.Repo, st); err != nil {
				return fmt.Errorf("record the pull request: %w", err)
			}
		}

		if !d.final {
			return r.continueIn(ctx, tx, id, detail, run.row.Checks, run.now)
		}

		finished, err = r.finishIn(ctx, tx, id, run.row.InvestigationID, d.state, detail, run.now)
		if err != nil {
			return fmt.Errorf("finish: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("in a transaction: %w", err)
	}

	if finished {
		r.metrics.count(run.reader.CIHost(), d.state)
		r.log.InfoContext(ctx, "CI reported", "proposal_id", id.String(), "host", run.reader.CIHost(), "state", d.state)
	}

	return nil
}

// continueIn stores the read of a report that is not final and queues the
// next read.
func (r *CIReporter) continueIn(ctx context.Context, tx pgx.Tx, id uuid.UUID, detail []byte, checks int32, now time.Time) error {
	if _, err := db.New(tx).RecordCICheck(ctx, db.RecordCICheckParams{ProposalID: id, Now: &now, Detail: detail}); err != nil {
		return fmt.Errorf("store the read: %w", err)
	}

	if err := enqueueCIJob(ctx, tx, id, now.Add(waitAfter(checks))); err != nil {
		return fmt.Errorf("queue the next read: %w", err)
	}

	return nil
}

// finishIn ends the report and queues its thread message. It reports whether
// this call ended it.
func (r *CIReporter) finishIn(ctx context.Context, tx pgx.Tx, id, investigationID uuid.UUID, state string, detail []byte, now time.Time) (bool, error) {
	rows, err := db.New(tx).FinishCIReport(ctx, db.FinishCIReportParams{ProposalID: id, State: state, Detail: detail, Now: &now})
	if err != nil {
		return false, fmt.Errorf("end the report: %w", err)
	}

	if rows == 0 {
		return false, nil
	}

	if err := publish.CIReported(ctx, tx, investigationID, id); err != nil {
		return false, fmt.Errorf("queue the thread message: %w", err)
	}

	return true, nil
}

// waitAfter is the wait after the read that follows checks reads: 30 seconds
// after the first, doubling up to 5 minutes.
func waitAfter(checks int32) time.Duration {
	wait := ciFirstWait

	for range min(checks, 8) {
		wait *= 2
		if wait >= ciMaxWait {
			return ciMaxWait
		}
	}

	return wait
}

// reportOf is what the card and the thread message show for a read and its
// decision.
func reportOf(run ciRun) output.CIReport {
	ci, d, pr := run.ci, run.decision, run.pr

	report := output.CIReport{
		State:         ci.State,
		Passed:        ci.Passed,
		Total:         ci.Total,
		FailedCount:   ci.FailedCount,
		Message:       ci.Message,
		Repo:          ci.Repo,
		Number:        pr.Number,
		URL:           ci.URL,
		WaitedSeconds: int(d.waited.Seconds()),
	}

	if d.final {
		report.State = d.state
	}

	if report.State == CIUnknown && report.Message == "" {
		report.Message = unknownMessage
	}

	for _, f := range ci.Failed[:min(len(ci.Failed), MaxFailedChecks)] {
		report.Failed = append(report.Failed, output.CICheck{Name: store.Text(f.Name), URL: store.Text(f.URL)})
	}

	return report
}

var (
	errNoProposal    = errors.New("the payload has no proposal ID")
	errNoReader      = errors.New("the action is not configured or cannot read CI")
	errNoPullRequest = errors.New("the proposal's result has no pull request")
)

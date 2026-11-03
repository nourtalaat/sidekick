//go:build integration

package gitchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of fixture/ci.sql.
var (
	ciInvestigation = uuid.MustParse("a2000000-0000-4000-8000-000000000001")
	ciProposal      = uuid.MustParse("c1000000-0000-4000-8000-000000000001")
	otherProposal   = uuid.MustParse("c1000000-0000-4000-8000-000000000002")
)

const (
	ciSink    = "slack-thread"
	ciTimeout = 2 * time.Hour
)

// ciStart is when the test reports start. It is in the past, so jobs queued
// for a time after it are ready at once.
var ciStart = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// ciEnv is a CI reporter over a database with one fake action.
type ciEnv struct {
	pool     *pgxpool.Pool
	reader   *fakeCI
	reporter *CIReporter
	counts   *CIMetrics
	clock    time.Time
}

func TestCIReportPassesAfterARunningRead(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIPending, Total: 3, Passed: 1, Repo: "acme/monitors", URL: "https://github.com/acme/monitors/pull/12"}}, ciAnswer{ci: CI{State: CIPassed, Total: 3, Passed: 3}})

	require.Equal(t, []string{"ci_report:" + ciProposal.String()}, env.activeKeys(t))

	require.NoError(t, env.step(t, ciStart))

	row := env.report(t, ciProposal)
	require.Equal(t, "pending", row.State)
	require.EqualValues(t, 1, row.Checks)
	require.Nil(t, row.ReportedAt)
	require.Equal(t, []string{"ci_report:" + ciProposal.String()}, env.activeKeys(t), "the next read is queued under the same key")
	require.True(t, ciStart.Add(30*time.Second).Equal(env.nextRunAt(t)), "the first wait is 30 seconds")

	pr, err := db.New(env.pool).GetPullRequestState(t.Context(), ciProposal)
	require.NoError(t, err)
	require.Equal(t, "acme/monitors", pr.Repo)
	require.EqualValues(t, 12, pr.Number)
	require.Equal(t, StateOpen, pr.State, "the first read records the pull request for the card")

	require.NoError(t, env.step(t, ciStart.Add(30*time.Second)))

	row = env.report(t, ciProposal)
	require.Equal(t, "passed", row.State)
	require.EqualValues(t, 2, row.Checks)
	require.NotNil(t, row.ReportedAt)
	require.Empty(t, env.activeKeys(t), "a final report queues no more reads")
	require.Equal(t, ciSeq(1), row.Seq, "the thread message has the first of the two seqs CIReported reserves")
	require.EqualValues(t, 2, env.publishSeq(t))
	require.InDelta(t, 1, testutil.ToFloat64(env.counts.reports.WithLabelValues("github", CIPassed)), 0)
	require.Equal(t, 2, env.reader.callCount())
}

func TestTwoWorkersAndARetriedJobReportOnce(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIFailed, Total: 4, Passed: 3, FailedCount: 1, Failed: []CICheck{{Name: "unit tests", URL: "https://ci.example.com/1"}}}})

	job := env.claim(t, ciStart)

	var wg sync.WaitGroup

	errs := make([]error, 2)

	for i := range errs {
		wg.Go(func() { errs[i] = env.reporter.Handle(t.Context(), job) })
	}

	wg.Wait()

	require.NoError(t, errors.Join(errs...))
	require.NoError(t, env.reporter.Handle(t.Context(), job), "a job that is run again after it ended reports nothing")

	require.NotNil(t, env.report(t, ciProposal).ReportedAt)
	require.Equal(t, ciSeq(1), env.report(t, ciProposal).Seq)
	require.EqualValues(t, 2, env.publishSeq(t), "the message was queued once")
	require.InDelta(t, 1, testutil.ToFloat64(env.counts.reports.WithLabelValues("github", CIFailed)), 0)

	var sent output.CIReport

	require.NoError(t, json.Unmarshal(env.report(t, ciProposal).Detail, &sent))
	require.Equal(t, output.CIReport{
		State: CIFailed, Passed: 3, Total: 4, FailedCount: 1, Number: 12,
		Failed: []output.CICheck{{Name: "unit tests", URL: "https://ci.example.com/1"}},
	}, sent)

	require.NoError(t, EnqueueCIReport(t.Context(), env.pool, ciProposal, ciStart))
	require.Empty(t, env.activeKeys(t), "starting a report that exists adds nothing")
}

func TestEnqueueingTwiceQueuesOneJob(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIPending}})

	require.NoError(t, EnqueueCIReport(t.Context(), env.pool, ciProposal, ciStart))
	require.Equal(t, []string{"ci_report:" + ciProposal.String()}, env.activeKeys(t))
}

func TestCIReportEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		answers []ciAnswer
		// at is when the last read happens; every earlier one is at ciStart.
		at        time.Duration
		wantState string
		wantMsg   string
		wantWait  int
	}{
		{
			name:      "nothing started within the grace",
			answers:   []ciAnswer{{ci: CI{State: CINone}}},
			at:        ciStartGrace,
			wantState: CINone,
			wantWait:  int(ciStartGrace.Seconds()),
		},
		{
			name:      "checks still running at the timeout",
			answers:   []ciAnswer{{ci: CI{State: CIPending, Total: 4, Passed: 1}}},
			at:        ciTimeout,
			wantState: CITimedOut,
			wantWait:  int(ciTimeout.Seconds()),
		},
		{
			name:      "a permission is missing",
			answers:   []ciAnswer{{ci: CI{State: CIUnknown, Message: "Sidekick could not read the checks. The GitHub App needs Checks (read)."}}},
			wantState: CIUnknown,
			wantMsg:   "Sidekick could not read the checks. The GitHub App needs Checks (read).",
		},
		{
			name:      "a read the host refuses for good",
			answers:   []ciAnswer{{err: fmt.Errorf("read: %w", action.ErrPermanent)}},
			wantState: CIUnknown,
			wantMsg:   unknownMessage,
		},
		{
			name:      "reads that keep failing end unknown at the timeout",
			answers:   []ciAnswer{{err: errors.New("rate limited")}},
			at:        ciTimeout,
			wantState: CIUnknown,
			wantMsg:   unknownMessage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newCIEnv(t, tt.answers...)

			if tt.at > 0 {
				require.NoError(t, env.step(t, ciStart))
				require.Nil(t, env.report(t, ciProposal).ReportedAt, "it is not final before then")
			}

			require.NoError(t, env.step(t, ciStart.Add(tt.at)))

			row := env.report(t, ciProposal)
			require.Equal(t, tt.wantState, row.State)
			require.NotNil(t, row.ReportedAt)

			var sent output.CIReport

			require.NoError(t, json.Unmarshal(row.Detail, &sent))
			require.Equal(t, tt.wantMsg, sent.Message)
			require.Equal(t, tt.wantWait, sent.WaitedSeconds)
		})
	}
}

func TestCIReportGivesUpWhenTheActionReportsNoCI(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIPassed}})

	require.NoError(t, EnqueueCIReport(t.Context(), env.pool, otherProposal, ciStart))

	job := env.claimKey(t, ciStart, "ci_report:"+otherProposal.String())

	err := env.reporter.Handle(t.Context(), job)
	require.ErrorIs(t, err, queue.ErrGiveUp)
	require.ErrorIs(t, err, errNoReader)
}

func TestCIReportOfAMissingProposalEndsQuietly(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIPassed}})

	payload, err := json.Marshal(ciPayload{ProposalID: uuid.New()})
	require.NoError(t, err)

	require.NoError(t, env.reporter.Handle(t.Context(), db.Job{Payload: payload}))
}

func TestCIReportOfAnUnreadablePayloadGivesUp(t *testing.T) {
	t.Parallel()

	env := newCIEnv(t, ciAnswer{ci: CI{State: CIPassed}})

	err := env.reporter.Handle(t.Context(), db.Job{Payload: []byte(`{}`)})
	require.ErrorIs(t, err, queue.ErrGiveUp)
}

// newCIEnv loads the fixture, opens the investigation's publish state for a
// Slack output, starts the report of ciProposal and returns the environment
// whose action answers with answers, the last one repeated.
func newCIEnv(t *testing.T, answers ...ciAnswer) *ciEnv {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/ci.sql")

	require.NoError(t, publish.Open(t.Context(), pool, ciInvestigation, []string{ciSink}))

	counts, err := NewCIMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	env := &ciEnv{pool: pool, reader: &fakeCI{answers: answers}, counts: counts, clock: ciStart}
	env.reporter = NewCIReporter(CIOptions{
		Pool:    pool,
		Actions: map[string]action.Action{"fix-code": env.reader},
		Metrics: counts,
		Now:     func() time.Time { return env.clock },
	})

	require.NoError(t, EnqueueCIReport(t.Context(), pool, ciProposal, ciStart))

	return env
}

// step claims the ready report job, runs it with the clock at at, and
// completes it as the queue runner does.
func (e *ciEnv) step(t *testing.T, at time.Time) error {
	t.Helper()

	job := e.claim(t, at)

	if err := e.reporter.Handle(t.Context(), job); err != nil {
		return fmt.Errorf("handle: %w", err)
	}

	_, err := db.New(e.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
	require.NoError(t, err)

	return nil
}

// claim claims the ready report job of ciProposal, with the clock at at.
func (e *ciEnv) claim(t *testing.T, at time.Time) db.Job {
	t.Helper()

	return e.claimKey(t, at, "ci_report:"+ciProposal.String())
}

// claimKey claims the ready report job with the unique key, with the clock
// at at.
func (e *ciEnv) claimKey(t *testing.T, at time.Time, key string) db.Job {
	t.Helper()

	e.clock = at

	jobs, err := db.New(e.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test", LeaseSeconds: time.Minute.Seconds(), Kinds: []string{queue.KindCIReport}, Batch: 10,
	})
	require.NoError(t, err)

	for _, job := range jobs {
		if job.UniqueKey != nil && *job.UniqueKey == key {
			return job
		}
	}

	require.FailNow(t, "no ready report job", key)

	return db.Job{}
}

// report returns the ci_reports row of the proposal.
func (e *ciEnv) report(t *testing.T, id uuid.UUID) ciRow {
	t.Helper()

	rows, err := e.pool.Query(t.Context(), "SELECT state, checks, detail, reported_at, seq FROM ci_reports WHERE proposal_id = $1", id)
	require.NoError(t, err)

	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[ciRow])
	require.NoError(t, err)

	return row
}

// ciRow is the columns of a ci_reports row the tests read.
type ciRow struct {
	State      string
	Checks     int32
	Detail     []byte
	ReportedAt *time.Time
	Seq        *int64
}

// ciSeq returns a pointer to n.
func ciSeq(n int64) *int64 {
	return &n
}

// activeKeys returns the unique keys of the pending and running report jobs.
func (e *ciEnv) activeKeys(t *testing.T) []string {
	t.Helper()

	rows, err := e.pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = $1 AND unique_key IS NOT NULL AND status IN ('pending', 'running') ORDER BY unique_key", queue.KindCIReport)
	require.NoError(t, err)

	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return keys
}

// nextRunAt returns when the pending report job runs.
func (e *ciEnv) nextRunAt(t *testing.T) time.Time {
	t.Helper()

	var at time.Time

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT run_at FROM jobs WHERE kind = $1 AND status = 'pending'", queue.KindCIReport).Scan(&at))

	return at.UTC()
}

// publishSeq returns the seq of the investigation's publish state, which
// each CIReported call raises by two.
func (e *ciEnv) publishSeq(t *testing.T) int64 {
	t.Helper()

	var seq int64

	require.NoError(t, e.pool.QueryRow(t.Context(), "SELECT seq FROM publish_states WHERE investigation_id = $1", ciInvestigation).Scan(&seq))

	return seq
}

// ciAnswer is what the fake action answers to one read.
type ciAnswer struct {
	ci  CI
	err error
}

// fakeCI is an action that reports CI. It answers with its answers in
// order, and with the last one after that.
type fakeCI struct {
	answers []ciAnswer

	mu    sync.Mutex
	calls int
}

var (
	_ action.Action = (*fakeCI)(nil)
	_ CIReader      = (*fakeCI)(nil)
)

func (f *fakeCI) Spec() action.Spec { return action.Spec{Name: "fix-code"} }

func (f *fakeCI) Validate(context.Context, json.RawMessage) error { return nil }

func (f *fakeCI) Execute(context.Context, action.Invocation) (action.Result, error) {
	return action.Result{}, nil
}

func (f *fakeCI) ReadCI(_ context.Context, params json.RawMessage, pr action.PullRequestRef) (CI, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if string(params) != `{"repo": "acme/monitors"}` || pr.Number != 12 || pr.HeadSHA != "4f2c1a9" {
		return CI{}, fmt.Errorf("unexpected call: params %s, pull request %+v: %w", params, pr, action.ErrPermanent)
	}

	a := f.answers[min(f.calls, len(f.answers)-1)]
	f.calls++

	return a.ci, a.err
}

func (f *fakeCI) CIReportTimeout() time.Duration { return ciTimeout }

func (f *fakeCI) CIHost() string { return "github" }

func (f *fakeCI) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

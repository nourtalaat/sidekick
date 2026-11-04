//go:build integration

package proposals

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The rows of fixture/investigations.sql.
var (
	checkoutInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	diskInvestigation     = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
	anaUser               = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d1")
	ciToken               = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000d2")
)

// monitorInput is a valid propose_action call for the fake monitor, which
// raises the threshold from 80 to 90.
const monitorInput = `{"action": "monitor", "params": {"threshold": 90}, "reason": "The alert fires on normal load."}`

// fixAPIInput and fixPullRequestInput are valid propose_action calls for
// the fake monitor under the names that report each kind of fix.
const (
	fixAPIInput         = `{"action": "monitor-fix", "params": {"threshold": 90}, "reason": "The alert fires on normal load."}`
	fixPullRequestInput = `{"action": "monitor-pr", "params": {"threshold": 90}, "reason": "The alert fires on normal load."}`
)

// noteInput is a valid propose_action call.
const noteInput = `{"action": "monitor-note", "params": {"board": "ops", "title": "Slow checkout"}, "reason": "p99 doubled after the deploy."}`

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Service on a database of its own, seeded with
// fixture/investigations.sql, with a clock the test moves and the route
// payments, which proposes the fake note action in approved mode.
type harness struct {
	pool *pgxpool.Pool
	svc  *Service
	note *fakeNote
	// monitor is the fake monitor action, a Snapshotter and Undoer that fixes
	// nothing. monitor-fix and monitor-pr are the same action reporting each
	// kind of fix.
	monitor *fakeMonitor
	// mute is the fake mute action, which creates what it undoes.
	mute   *fakeMute
	policy *movablePolicy
	clock  *clock
}

// movablePolicy is a Policy whose routes a test changes while it runs.
type movablePolicy struct {
	mu     sync.Mutex
	routes action.StaticPolicy
}

func (p *movablePolicy) Route(name string) (action.RoutePolicy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.routes.Route(name)
}

func (p *movablePolicy) set(name string, r action.RoutePolicy) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.routes[name] = r
}

// clock is a clock the test moves.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	for _, id := range []uuid.UUID{checkoutInvestigation, diskInvestigation} {
		require.NoError(t, publish.Open(t.Context(), pool, id, []string{"slack"}))
	}

	h := &harness{
		pool:    pool,
		note:    newFakeNote("monitor-note"),
		monitor: newFakeMonitor("monitor"),
		mute:    newFakeMute("mute"),
		clock:   &clock{now: time.Now().UTC().Truncate(time.Second)},
		policy: &movablePolicy{routes: action.StaticPolicy{
			"payments": {Mode: action.Approved, Actions: []string{"monitor-note", "monitor", "monitor-fix", "monitor-pr", "mute", "ci-note", "pr-note"}},
			"default":  {Mode: action.ReadOnly},
		}},
	}

	h.svc = h.service(t, Options{})

	return h
}

// service returns a Service on the harness's pool, action, policy and
// clock, with the other options from opts.
func (h *harness) service(t *testing.T, opts Options) *Service {
	t.Helper()

	opts.Pool, opts.Policy, opts.Now = h.pool, h.policy, h.clock.Now
	opts.Actions = map[string]action.Action{
		"monitor-note": h.note,
		"monitor":      h.monitor,
		"monitor-fix":  fixingMonitor{fakeMonitor: newFakeMonitor("monitor-fix"), kind: action.FixAPI},
		"monitor-pr":   fixingMonitor{fakeMonitor: newFakeMonitor("monitor-pr"), kind: action.FixPullRequest},
		"mute":         h.mute,
		"ci-note":      newCINote("ci-note", true),
		"pr-note":      newCINote("pr-note", false),
	}

	s, err := New(opts)
	require.NoError(t, err)

	return s
}

// propose makes the call with the ID callID on the checkout investigation
// and requires that it is stored.
func (h *harness) propose(t *testing.T, callID, input string) uuid.UUID {
	t.Helper()

	res, err := h.svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: callID, Route: "payments", Mode: action.Approved, Input: json.RawMessage(input),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)
	require.NotEqual(t, uuid.Nil, res.ID)

	return res.ID
}

// row returns the proposal row with id.
func (h *harness) row(t *testing.T, id uuid.UUID) db.ActionProposal {
	t.Helper()

	row, err := db.New(h.pool).GetActionProposal(t.Context(), id)
	require.NoError(t, err)

	return row
}

// auditActions returns the audit actions written for the proposal with
// id, oldest first.
func (h *harness) auditActions(t *testing.T, id uuid.UUID) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT action FROM audit_log WHERE target_kind = 'action_proposal' AND target_id = $1 ORDER BY id", id.String())
	require.NoError(t, err)

	actions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return actions
}

// jobs returns the execute_action jobs that are not done.
func (h *harness) jobs(t *testing.T) []db.Job {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM jobs WHERE kind = $1 ORDER BY id", queue.KindExecuteAction)
	require.NoError(t, err)

	jobs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.Job])
	require.NoError(t, err)

	return jobs
}

// publishSeq returns the seq of the investigation's publish state.
func (h *harness) publishSeq(t *testing.T, id uuid.UUID) int64 {
	t.Helper()

	var seq int64
	require.NoError(t, h.pool.QueryRow(context.Background(), "SELECT seq FROM publish_states WHERE investigation_id = $1", id).Scan(&seq))

	return seq
}

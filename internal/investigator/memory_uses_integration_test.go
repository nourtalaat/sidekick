//go:build integration

package investigator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
)

// The memory entries of insertMemoryExamples.
var (
	entryFingerprint = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")
	entryServiceName = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e2")
	entryWords       = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e3")
)

// insertMemoryExamples adds three confirmed entries that match the fixture's
// alert, each in a different way: the same fingerprint, the same service
// and alert name, and words in common.
const insertMemoryExamples = `INSERT INTO memory_entries (investigation_id, service, alert_name, fingerprint, verdict, summary, cause, confirmed_at)
VALUES
  ('0192d3a4-0000-7000-8000-0000000000e1', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout', 'true_positive',
   'p99 latency rose after a deploy.', 'Deploy 9a8b7c added a slow query.', '2026-10-20 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000e2', 'checkout-api', 'CheckoutLatencyP99', 'fp-checkout-eu', 'true_positive',
   'p99 latency rose in the EU region.', 'A node pool ran out of memory.', '2026-10-19 10:00:00+00'),
  ('0192d3a4-0000-7000-8000-0000000000e3', 'orders-api', 'OrdersSlow', 'fp-orders', 'false_positive',
   'Latency was high only on the batch export endpoint.', NULL, '2026-10-18 10:00:00+00')`

// memoryUse is a row of memory_uses as the tests compare it.
type memoryUse struct {
	example uuid.UUID
	match   prompt.MemoryMatch
	rank    int
	run     int
}

// threeUses are the rows insertMemoryExamples leads to in run 1.
var threeUses = []memoryUse{
	{entryFingerprint, prompt.MatchFingerprint, 1, 1},
	{entryServiceName, prompt.MatchServiceAlert, 2, 1},
	{entryWords, prompt.MatchText, 3, 1},
}

func TestRunRecordsMemoryUses(t *testing.T) {
	t.Parallel()

	verdictTurns := []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, notSure))}

	tests := []struct {
		name     string
		opts     options
		seed     bool
		examples int
		want     []memoryUse
	}{
		{
			name:     "the examples of the prompt are recorded with their match and rank",
			opts:     options{turns: verdictTurns},
			seed:     true,
			examples: 3,
			want:     threeUses,
		},
		{
			name:     "the limit decides how many are recorded",
			opts:     options{turns: verdictTurns},
			seed:     true,
			examples: 2,
			want:     threeUses[:2],
		},
		{
			name:     "memory turned off records none",
			opts:     options{turns: verdictTurns},
			seed:     true,
			examples: 0,
		},
		{
			name:     "no entry like the alert records none",
			opts:     options{turns: verdictTurns},
			examples: 3,
		},
		{
			name: "the full investigation after triage records its examples, and the triage prompt none",
			opts: options{
				triageTurns: []modeltest.Turn{triageTurn(triageLatency)},
				turns:       verdictTurns,
			},
			seed:     true,
			examples: 3,
			want:     threeUses,
		},
		{
			name:     "a run that triage ends records none",
			opts:     options{triageTurns: []modeltest.Turn{triageTurn(triageDuplicate)}},
			seed:     true,
			examples: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.opts)
			h.handler.opts.MemoryExamples = tt.examples

			if tt.seed {
				_, err := h.pool.Exec(t.Context(), insertMemoryExamples)
				require.NoError(t, err)
			}

			require.NoError(t, h.run(t))

			require.Equal(t, tt.want, h.memoryUses(t))

			if len(tt.want) == 0 {
				return
			}

			steps := h.steps(t)
			require.EqualValues(t, tt.want[0].run, steps[len(steps)-3].Run, "the rows carry the run of the prompt step")

			first := userText(h.conn.Requests()[0].Messages[0])
			require.Contains(t, first, "# Similar past investigations")
		})
	}
}

// TestRunRecordsMemoryUsesOnce stops the worker after the prompt step and
// resumes the run with a second worker: the examples are recorded once.
func TestRunRecordsMemoryUsesOnce(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{turn(search("c1")), turn(search("c2"))}})
	h.handler.opts.MemoryExamples = 3

	_, err := h.pool.Exec(t.Context(), insertMemoryExamples)
	require.NoError(t, err)

	blocked := make(chan struct{})
	h.handler = newHandler(t, h.pool, h.conn, blockingSignoz(t, blocked), h.spans, prometheus.NewRegistry())
	h.handler.opts.MemoryExamples = 3

	ctx, stop := context.WithCancel(t.Context())

	go func() {
		<-blocked
		stop()
	}()

	require.Error(t, h.handler.Handle(ctx, h.claim(t)), "the stopped worker returns an error")
	require.Equal(t, threeUses, h.memoryUses(t), "the first worker recorded the examples with its prompt")

	h.expireLease(t)

	second := modeltest.New(t, model.Capabilities{ToolCalling: true}, turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)))
	second.ExpectAllUsed()
	h.handler = newHandler(t, h.pool, second, fakeSignoz(t), h.spans, prometheus.NewRegistry())
	h.handler.opts.MemoryExamples = 3

	require.NoError(t, h.run(t))
	require.Equal(t, threeUses, h.memoryUses(t), "the second worker recorded nothing more")
}

// TestStaleFenceRecordsNoMemoryUses checks that a worker whose claim is
// older than the one that owns the run cannot save a prompt, so its
// examples are not recorded either.
func TestStaleFenceRecordsNoMemoryUses(t *testing.T) {
	t.Parallel()

	h := setup(t, options{})

	stale, started, err := h.handler.start(t.Context(), testInvestigation, 10)
	require.NoError(t, err)
	require.True(t, started)

	_, started, err = h.handler.start(t.Context(), testInvestigation, 11)
	require.NoError(t, err)
	require.True(t, started, "a newer claim takes over the run")

	req := model.Request{}
	examples := []prompt.Example{{InvestigationID: entryFingerprint.String(), Match: prompt.MatchFingerprint}}

	_, err = staleRun(t, h, stale).save(t.Context(), newStep{kind: kindPrompt, prompt: &req, status: statusOK, startedAt: time.Now(), memory: examples})
	require.ErrorIs(t, err, queue.ErrLeaseLost)
	require.Empty(t, h.memoryUses(t))
	require.Empty(t, h.steps(t))
}

// memoryUses returns the memory uses of the test investigation, by rank
// within each run. It returns nil when there are none.
func (h *harness) memoryUses(t *testing.T) []memoryUse {
	t.Helper()

	rows, err := h.pool.Query(t.Context(),
		"SELECT example_id, match, rank, run FROM memory_uses WHERE investigation_id = $1 ORDER BY run, rank", testInvestigation)
	require.NoError(t, err)

	defer rows.Close()

	var out []memoryUse

	for rows.Next() {
		var u memoryUse

		require.NoError(t, rows.Scan(&u.example, &u.match, &u.rank, &u.run))

		out = append(out, u)
	}

	require.NoError(t, rows.Err())

	return out
}

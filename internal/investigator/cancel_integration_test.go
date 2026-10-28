//go:build integration

package investigator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// stopWithin is how soon a stopped run must end.
const stopWithin = 5 * time.Second

// stopper is a responder who presses Stop.
var stopper = Requester{SlackUserID: "U01STOPPER"}

// TestCancel checks that a Stop asked for on another replica ends the run
// as cancelled, with a report from the saved steps, whatever the run is
// doing at that moment.
func TestCancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// turns are the model's turns; started is closed when the run is
		// in the slow call that the Stop must cut short.
		turns func(started chan<- struct{}) []modeltest.Turn
		// slowTool makes the second source call answer only when its
		// context ends, closing started when it begins.
		slowTool bool
		// before asks for the stop before the run starts.
		before bool
		steps  []string
	}{
		{
			name: "during a slow model call",
			turns: func(started chan<- struct{}) []modeltest.Turn {
				return []modeltest.Turn{turn(search("c1")), slowTurn(started)}
			},
			steps: []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs"},
		},
		{
			name:     "during a slow tool call",
			slowTool: true,
			turns: func(chan<- struct{}) []modeltest.Turn {
				return []modeltest.Turn{turn(search("c1")), turn(search("c2"))}
			},
			steps: []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok "},
		},
		{
			name:   "before the run starts",
			before: true,
			turns:  func(chan<- struct{}) []modeltest.Turn { return nil },
			steps:  []string{"prompt ok "},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{})
			h := setup(t, options{turns: tt.turns(started)})

			if tt.slowTool {
				h.handler = newHandler(t, h.pool, h.conn, blockingSignoz(t, started), h.spans, prometheus.NewRegistry())
			}

			canceller := NewCanceller(h.pool, nil)
			h.handler.opts.Canceller = canceller

			go func() { _ = canceller.Run(t.Context()) }()

			other := otherReplica(t, h.pool)

			if tt.before {
				require.True(t, requestCancel(t, other))
				close(started)
			}

			job := h.claim(t)
			done := make(chan error, 1)

			go func() { done <- h.handler.Handle(t.Context(), job) }()

			<-started

			if !tt.before {
				require.True(t, requestCancel(t, other))
			}

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(stopWithin):
				t.Fatalf("the run did not end within %s of the Stop", stopWithin)
			}

			inv := h.investigation(t)
			require.Equal(t, StatusCancelled, inv.Status)
			require.Equal(t, string(report.StopCancelled), deref(inv.StopReason))
			require.Equal(t, stopper.SlackUserID, deref(inv.CancelRequestedBySlackUser))
			require.NotNil(t, inv.FinishedAt)
			require.Equal(t, tt.steps, summary(h.steps(t)), "the run saves nothing after the Stop")

			r := h.report(t)
			require.Equal(t, report.StopCancelled, r.StopReason)
			require.Equal(t, report.Inconclusive, r.Verdict)
			require.Equal(t, cancelledReason, r.Reason)

			_, phase, body := h.publishState(t)
			require.Equal(t, "final", phase)
			require.Equal(t, report.StopCancelled, body.Report.StopReason)

			require.False(t, requestCancel(t, other), "an ended investigation cannot be stopped")
		})
	}
}

// TestRequestCancel checks that a second Stop changes nothing.
func TestRequestCancel(t *testing.T) {
	t.Parallel()

	h := setup(t, options{})

	require.True(t, requestCancel(t, h.pool))

	first := h.investigation(t).CancelRequestedAt
	require.NotNil(t, first)

	require.False(t, requestCancel(t, h.pool), "a repeated Stop")
	require.Equal(t, first, h.investigation(t).CancelRequestedAt)
	require.Equal(t, []string{"user U01STOPPER"}, h.audited(t, ActionCancelRequested), "only the Stop that took effect is audited")
}

func TestRequestCancelAuditsTheKindOfRequester(t *testing.T) {
	t.Parallel()

	byUser := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f1")

	tests := []struct {
		name string
		by   Requester
		want string
	}{
		{name: "a web user", by: Requester{UserID: &byUser}, want: "user " + byUser.String()},
		{name: "an API token", by: Requester{UserID: &byUser, Token: true}, want: "token " + byUser.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{})

			err := pgx.BeginFunc(t.Context(), h.pool, func(tx pgx.Tx) error {
				_, err := RequestCancel(t.Context(), tx, testInvestigation, tt.by)

				return err
			})
			require.NoError(t, err)
			require.Equal(t, []string{tt.want}, h.audited(t, ActionCancelRequested))
		})
	}
}

// audited returns "actor_kind actor_id" of each audit entry with action.
func (h *harness) audited(t *testing.T, action string) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(),
		"SELECT actor_kind || ' ' || coalesce(actor_id, '') FROM audit_log WHERE action = $1 AND investigation_id = $2 ORDER BY id", action, testInvestigation)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return out
}

// slowTurn is a model turn that closes started and answers only when its
// context ends.
func slowTurn(started chan<- struct{}) modeltest.Turn {
	return modeltest.Turn{Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
		close(started)
		<-ctx.Done()

		return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: ctx.Err()}
	}}
}

// otherReplica returns a pool of its own on the same database, as another
// replica has.
func otherReplica(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()

	other, err := pgxpool.NewWithConfig(t.Context(), pool.Config().Copy())
	require.NoError(t, err)
	t.Cleanup(other.Close)

	return other
}

// beginner starts transactions.
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// requestCancel asks to stop the test investigation as stopper, in a
// transaction of its own on pool, and returns what RequestCancel returned.
func requestCancel(t *testing.T, pool beginner) bool {
	t.Helper()

	var asked bool

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		var err error
		asked, err = RequestCancel(t.Context(), tx, testInvestigation, stopper)

		return err
	})
	require.NoError(t, err)

	return asked
}

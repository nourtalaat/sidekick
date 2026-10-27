//go:build integration

package investigator

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The events of fixture/inbox.sql.
var (
	joinedEvent    = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a2")
	recoveredEvent = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a3")
)

// inboxHeading starts the message that delivers the inbox.
const inboxHeading = "# New since your last turn"

func TestInboxDelivery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// before adds the items before the run starts; otherwise they are
		// added while the model takes its first turn.
		before bool
		steps  []string
		// wantRequest is the request whose last message holds the inbox.
		wantRequest  int
		wantDelivery int32
	}{
		{
			name:         "items that wait when the run starts",
			before:       true,
			steps:        []string{"prompt ok ", "inbox ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok ", "tool_result ok submit_verdict"},
			wantRequest:  0,
			wantDelivery: 1,
		},
		{
			name:         "items that arrive during a model turn",
			steps:        []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "inbox ok ", "model_turn ok ", "tool_result ok submit_verdict"},
			wantRequest:  1,
			wantDelivery: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var h *harness

			first := turn(search("c1"))
			if !tt.before {
				scripted := first.Response
				first = modeltest.Turn{Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
					h.addInbox(t, ctx)

					return scripted, nil
				}}
			}

			h = setup(t, options{turns: []modeltest.Turn{
				first,
				turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
			}})
			h.loadInboxEvents(t)

			if tt.before {
				h.addInbox(t, t.Context())
			}

			require.NoError(t, h.run(t))
			require.Equal(t, tt.steps, summary(h.steps(t)))

			requests := h.conn.Requests()
			require.Len(t, requests, 2)

			for i, req := range requests {
				want := 0
				if i >= tt.wantRequest {
					want = 1
				}

				require.Equal(t, want, inboxMessages(req), "request %d", i)
			}

			delivering := requests[tt.wantRequest].Messages
			text := userText(delivering[len(delivering)-1])
			require.Contains(t, text, inboxHeading, "the inbox is in the newest user message")
			require.Contains(t, text, "1 new event joined the group.")
			require.Contains(t, text, "1 recovery arrived.")
			require.Contains(t, text, `<untrusted_data source="alertmanager">`)

			var waiting int

			require.NoError(t, h.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM investigation_inbox WHERE delivered_seq IS NULL").Scan(&waiting))
			require.Zero(t, waiting)

			var delivered []int32

			rows, err := h.pool.Query(t.Context(), "SELECT delivered_seq FROM investigation_inbox ORDER BY id")
			require.NoError(t, err)

			for rows.Next() {
				var seq int32
				require.NoError(t, rows.Scan(&seq))

				delivered = append(delivered, seq)
			}

			require.NoError(t, rows.Err())
			require.Equal(t, []int32{tt.wantDelivery, tt.wantDelivery}, delivered, "both items are delivered by one step")
		})
	}
}

// TestInboxIsDeliveredOnceAcrossAResume stops the worker while a source
// call of the second model turn is in flight, after the inbox step is
// saved; the worker that resumes sends the saved step and delivers nothing
// again.
func TestInboxIsDeliveredOnceAcrossAResume(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{turn(search("c1")), turn(search("c2"))}})
	h.loadInboxEvents(t)
	h.addInbox(t, t.Context())

	blocked := make(chan struct{})
	h.handler = newHandler(t, h.pool, h.conn, blockingSignoz(t, blocked), h.spans, prometheus.NewRegistry())

	ctx, stop := context.WithCancel(t.Context())

	go func() {
		<-blocked
		stop()
	}()

	require.Error(t, h.handler.Handle(ctx, h.claim(t)), "the stopped worker returns an error")
	require.Equal(t, []string{"prompt ok ", "inbox ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok "}, summary(h.steps(t)))

	h.expireLease(t)

	resumed := modeltest.New(t, model.Capabilities{ToolCalling: true}, turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)))
	resumed.ExpectAllUsed()
	h.handler = newHandler(t, h.pool, resumed, fakeSignoz(t), h.spans, prometheus.NewRegistry())

	require.NoError(t, h.run(t))
	require.Equal(t, []string{
		"prompt ok ", "inbox ok ", "model_turn ok ", "tool_result ok search_logs",
		"model_turn ok ", "tool_result ok search_logs", "model_turn ok ", "tool_result ok submit_verdict",
	}, summary(h.steps(t)))

	requests := resumed.Requests()
	require.Len(t, requests, 1)
	require.Equal(t, 1, inboxMessages(requests[0]), "the resumed run sends the saved inbox step once")
}

// loadInboxEvents adds the events of fixture/inbox.sql.
func (h *harness) loadInboxEvents(t *testing.T) {
	t.Helper()

	storetest.LoadFixture(t, h.pool, "fixture/inbox.sql")
}

// addInbox adds the joined and the recovered event to the inbox of the
// test investigation.
func (h *harness) addInbox(t *testing.T, ctx context.Context) {
	t.Helper()

	require.NoError(t, AddEventToInbox(ctx, h.pool, testInvestigation, InboxNewEvent, joinedEvent))
	require.NoError(t, AddEventToInbox(ctx, h.pool, testInvestigation, InboxResolved, recoveredEvent))
}

// userText joins the text parts of m.
func userText(m model.Message) string {
	var b strings.Builder

	for _, p := range m.Parts {
		if text, ok := p.(model.Text); ok {
			b.WriteString(text.Text)
		}
	}

	return b.String()
}

// inboxMessages counts the inbox messages in req.
func inboxMessages(req model.Request) int {
	n := 0
	for _, m := range req.Messages {
		n += strings.Count(userText(m), inboxHeading)
	}

	return n
}

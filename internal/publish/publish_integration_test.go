//go:build integration

package publish

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// pollInterval is how often the runner in the tests looks for jobs.
const pollInterval = 50 * time.Millisecond

func TestOpenWritesTheQueuedStateAndAJobPerSink(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink, "callback")

	state := h.state(t, webhookInvestigation)
	require.Equal(t, []string{slackSink, "callback"}, state.Sinks)
	require.Equal(t, SeqQueued, state.Seq)
	require.Equal(t, string(output.PhaseQueued), state.Phase)

	jobs := h.jobs(t)
	require.Len(t, jobs, 2)

	for i, sink := range []string{slackSink, "callback"} {
		require.Equal(t, queue.StatusPending, jobs[i].Status)
		require.Equal(t, UniqueKey(webhookInvestigation, sink), *jobs[i].UniqueKey)
		require.Equal(t, int32(jobMaxAttempts), jobs[i].MaxAttempts)
		require.JSONEq(t, `{"investigation_id":"`+webhookInvestigation.String()+`","sink":"`+sink+`"}`, string(jobs[i].Payload))
	}

	h.open(t, webhookInvestigation, "other")
	require.Equal(t, []string{slackSink, "callback"}, h.state(t, webhookInvestigation).Sinks, "a second Open changes nothing")
	require.Len(t, h.jobs(t), 2)
}

func TestEnqueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// before are the updates written first.
		before    []Update
		update    Update
		wantSeq   int64
		wantPhase output.Phase
		wantJobs  int
	}{
		{
			name:      "a newer update",
			update:    Update{InvestigationID: webhookInvestigation, Phase: output.PhaseRunning, Seq: 1},
			wantSeq:   1,
			wantPhase: output.PhaseRunning,
			wantJobs:  1,
		},
		{
			name:      "an update that is not newer",
			before:    []Update{{InvestigationID: webhookInvestigation, Phase: output.PhaseFinal, Seq: 5}},
			update:    Update{InvestigationID: webhookInvestigation, Phase: output.PhaseRunning, Seq: 4},
			wantSeq:   5,
			wantPhase: output.PhaseFinal,
			wantJobs:  1,
		},
		{
			name:      "an investigation without a publish state",
			update:    Update{InvestigationID: slackInvestigation, Phase: output.PhaseRunning, Seq: 1},
			wantSeq:   SeqQueued,
			wantPhase: output.PhaseQueued,
			wantJobs:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.open(t, webhookInvestigation, slackSink)

			for _, u := range tt.before {
				h.enqueue(t, u)
			}

			h.enqueue(t, tt.update)

			state := h.state(t, webhookInvestigation)
			require.Equal(t, tt.wantSeq, state.Seq)
			require.Equal(t, string(tt.wantPhase), state.Phase)
			require.Len(t, h.jobs(t), tt.wantJobs, "a waiting job is kept, not doubled")
		})
	}
}

func TestEnqueueStoresTheProgressAndReport(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)

	at := time.Date(2026, 10, 26, 14, 2, 30, 0, time.UTC)
	h.enqueue(t, Update{
		InvestigationID: webhookInvestigation,
		Phase:           output.PhaseFinal,
		Seq:             3,
		Progress:        []output.Progress{{At: at, Text: "Read the alert"}},
		Report:          &report.Report{Verdict: report.Inconclusive, Confidence: report.Low, Reason: "Not enough data."},
	})

	require.JSONEq(t, `{
		"progress": [{"at": "2026-10-26T14:02:30Z", "text": "Read the alert"}],
		"report": {"verdict": "inconclusive", "confidence": "low", "reason": "Not enough data.", "summary": "", "findings": null, "checked": null, "stopReason": ""}
	}`, string(h.state(t, webhookInvestigation).Body))
}

func TestHandleFirstPost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		investigation string
		wantChannel   string
		wantThreadTS  string
		wantContext   string
	}{
		{name: "a webhook alert", investigation: webhookInvestigation.String(), wantChannel: "#alerts"},
		{
			name:          "an alert posted in Slack",
			investigation: slackInvestigation.String(),
			wantChannel:   "C999",
			wantThreadTS:  "1793000000.000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			id := webhookInvestigation
			if tt.investigation == slackInvestigation.String() {
				id = slackInvestigation
			}

			h.open(t, id, slackSink)
			require.NoError(t, h.run(t))

			posts := h.slack.callsTo("chat.postMessage")
			require.Len(t, posts, 1)
			require.Equal(t, tt.wantChannel, posts[0].Channel)
			require.Equal(t, tt.wantThreadTS, posts[0].ThreadTS)
			require.True(t, strings.HasPrefix(posts[0].Text, "Looking into it: "), posts[0].Text)

			ref := h.outputRef(t, id, slackSink)
			require.Equal(t, channelID(tt.wantChannel)+":1793023321.000001", ref.External)
			require.Equal(t, SeqQueued, ref.LastSeq)

			var stored output.Ref
			require.NoError(t, json.Unmarshal(ref.Ref, &stored))
			require.Equal(t, slackSink, stored.Sink)
			require.Equal(t, ref.External, stored.External)

			jobs := h.jobs(t)
			require.Len(t, jobs, 1)
			require.Equal(t, queue.StatusDone, jobs[0].Status)
			require.Nil(t, jobs[0].UniqueKey, "the finished job gave up its key")

			spans := h.spans.Ended()
			require.Len(t, spans, 1)
			require.Equal(t, SpanName, spans[0].Name())
			require.Contains(t, spans[0].Attributes(), attribute.String(attrSink, slackSink))
			require.Contains(t, spans[0].Attributes(), attribute.String(attrPhase, string(output.PhaseQueued)))
		})
	}
}

func TestHandleSkipsAStateTheSinkShows(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	require.NoError(t, enqueueJob(t.Context(), h.pool, Payload{InvestigationID: webhookInvestigation, Sink: slackSink}))
	require.NoError(t, h.run(t))

	require.Len(t, h.slack.callsTo("chat.postMessage"), 1)
	require.Empty(t, h.slack.callsTo("chat.update"))
}

func TestHandleShowsOnlyTheNewestOfWaitingUpdates(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	for seq := int64(1); seq <= 3; seq++ {
		h.enqueue(t, Update{InvestigationID: webhookInvestigation, Phase: output.PhaseRunning, Seq: seq, Progress: progress(int(seq))})
	}

	require.Len(t, h.jobs(t), 2, "the three updates share one waiting job")
	require.NoError(t, h.run(t))

	edits := h.slack.callsTo("chat.update")
	require.Len(t, edits, 1)
	require.Equal(t, "Investigating: checkout-api p99 latency above 2s", edits[0].Text)
	require.Equal(t, int64(3), h.outputRef(t, webhookInvestigation, slackSink).LastSeq)
}

func TestHandleGivesUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantErr error
	}{
		{name: "a payload that does not decode", payload: `{"investigation_id": 7}`},
		{name: "no investigation", payload: `{"sink": "slack-thread"}`, wantErr: errNoInvestigation},
		{name: "an output that is not configured", payload: `{"investigation_id": "` + webhookInvestigation.String() + `", "sink": "gone"}`, wantErr: errUnknownSink},
		{name: "no publish state", payload: `{"investigation_id": "` + slackInvestigation.String() + `", "sink": "slack-thread"}`, wantErr: errNoState},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			_, err := queue.Enqueue(t.Context(), h.pool, queue.NewJob{Kind: queue.KindPublish, Payload: json.RawMessage(tt.payload)})
			require.NoError(t, err)

			err = h.run(t)
			require.ErrorIs(t, err, queue.ErrGiveUp)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

func TestHandleKeepsTheKeyWhenTheSinkFails(t *testing.T) {
	t.Parallel()

	h := setup(t)

	sink := output.NewMockSink(gomock.NewController(t))
	sink.EXPECT().Accepts(output.PhaseWatcher).Return(false)
	sink.EXPECT().Accepts(output.PhaseQueued).Return(true)
	sink.EXPECT().Publish(gomock.Any(), gomock.Any()).Return(output.Ref{}, errors.New("slack is down"))

	h = setupWith(t, h.pool, h.slack, map[string]output.Sink{slackSink: sink})
	h.open(t, webhookInvestigation, slackSink)

	err := h.run(t)
	require.ErrorContains(t, err, "slack is down")
	require.NotErrorIs(t, err, queue.ErrGiveUp)

	jobs := h.jobs(t)
	require.Equal(t, queue.StatusRunning, jobs[0].Status, "the runner, not the handler, fails the job")
	require.Equal(t, UniqueKey(webhookInvestigation, slackSink), *jobs[0].UniqueKey, "the job keeps its key until it is retried")
	require.Empty(t, collect[db.OutputRef](t, h.pool, "SELECT * FROM output_refs"))
}

func TestHandleSkipsAPhaseTheSinkDoesNotShow(t *testing.T) {
	t.Parallel()

	h := setup(t)

	sink := output.NewMockSink(gomock.NewController(t))
	sink.EXPECT().Accepts(output.PhaseWatcher).Return(false)
	sink.EXPECT().Accepts(output.PhaseQueued).Return(false)

	h = setupWith(t, h.pool, h.slack, map[string]output.Sink{"callback": sink})
	h.open(t, webhookInvestigation, "callback")

	require.NoError(t, h.run(t))
	require.Empty(t, collect[db.OutputRef](t, h.pool, "SELECT * FROM output_refs"))
	require.Nil(t, h.jobs(t)[0].UniqueKey)
}

// TestFinalDuringARunningPublishIsPostedOnce runs the real queue runner.
// A progress publish is held inside its Slack call while the final update
// arrives, which finds the unique key taken. When the progress publish
// ends, it sees the newer state and enqueues the next job, which posts the
// final verdict and announces it once.
func TestFinalDuringARunningPublishIsPostedOnce(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	held, release := h.slack.holdNextEdit()
	h.enqueue(t, Update{InvestigationID: webhookInvestigation, Phase: output.PhaseRunning, Seq: 1, Progress: progress(1)})

	h.startRunner(t)

	select {
	case <-held:
	case <-time.After(waitTimeout):
		t.Fatal("the progress publish did not reach Slack")
	}

	_, err := queue.Enqueue(t.Context(), h.pool, queue.NewJob{
		Kind: queue.KindPublish, Payload: json.RawMessage(`{}`), UniqueKey: UniqueKey(webhookInvestigation, slackSink),
	})
	require.ErrorIs(t, err, queue.ErrDuplicate, "the running progress publish holds the key")

	h.enqueue(t, Update{
		InvestigationID: webhookInvestigation,
		Phase:           output.PhaseFinal,
		Seq:             2,
		Progress:        progress(2),
		Report:          &report.Report{Verdict: report.TruePositive, Confidence: report.High, Reason: "Deploy 4f2c1a broke checkout."},
	})

	running := h.jobs(t)
	require.Len(t, running, 2, "the final update added no job")
	require.Equal(t, queue.StatusRunning, running[1].Status)

	close(release)

	require.Eventually(t, func() bool {
		jobs := h.jobs(t)

		return len(jobs) == 3 && jobs[2].Status == queue.StatusDone
	}, waitTimeout, pollInterval, "the next job is enqueued and runs")

	edits := h.slack.callsTo("chat.update")
	require.Len(t, edits, 2)
	require.Equal(t, "Investigating: checkout-api p99 latency above 2s", edits[0].Text)
	require.Equal(t, "Real issue (high confidence): checkout-api p99 latency above 2s", edits[1].Text)

	require.Equal(t, []string{"Real issue (high confidence): checkout-api p99 latency above 2s"}, replies(h.slack), "the verdict is announced once")
	require.Equal(t, int64(2), h.outputRef(t, webhookInvestigation, slackSink).LastSeq)

	for _, j := range h.jobs(t) {
		require.Equal(t, queue.StatusDone, j.Status)
	}
}

func TestARetriedFinalIsAnnouncedOnce(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.open(t, webhookInvestigation, slackSink)
	require.NoError(t, h.run(t))

	final := Update{
		InvestigationID: webhookInvestigation,
		Phase:           output.PhaseFinal,
		Seq:             1,
		Report:          &report.Report{Verdict: report.FalsePositive, Confidence: report.High, Reason: "A nightly job."},
	}

	h.enqueue(t, final)
	require.NoError(t, h.run(t))

	final.Seq = 2
	h.enqueue(t, final)
	require.NoError(t, h.run(t))

	require.Len(t, h.slack.callsTo("chat.update"), 2, "both finals edit the message")
	require.Equal(t, []string{"False alarm (high confidence): checkout-api p99 latency above 2s"}, replies(h.slack))
}

// startRunner runs a queue runner with h's handler until the test ends.
func (h *harness) startRunner(t *testing.T) {
	t.Helper()

	runner, err := queue.NewRunner(h.pool, queue.Options{PollInterval: pollInterval, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	require.NoError(t, runner.Register(queue.KindPublish, h.handler, queue.KindOptions{Concurrency: 2}))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- runner.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

// replies returns the text of the thread replies the fake Slack got.
func replies(f *fakeSlack) []string {
	var out []string

	for _, p := range f.callsTo("chat.postMessage") {
		if p.ThreadTS != "" {
			out = append(out, p.Text)
		}
	}

	return out
}

// progress returns n progress lines.
func progress(n int) []output.Progress {
	out := make([]output.Progress, 0, n)
	for i := range n {
		out = append(out, output.Progress{At: time.Date(2026, 10, 26, 14, 2, 10+i, 0, time.UTC), Text: "Checked step " + string(rune('A'+i))})
	}

	return out
}

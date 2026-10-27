//go:build integration

package publish

import (
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// concurrentSenders is how many transactions try to send the same notice
// at once.
const concurrentSenders = 8

// noticeAt is when the test notices are first sent.
var noticeAt = time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)

// testNotice is a daily cap notice for the Slack output.
var testNotice = Notice{
	Key:       "daily_cap:route:payments",
	At:        noticeAt,
	NotBefore: noticeAt.Add(-time.Hour),
	Sinks:     []string{slackSink},
	Notice:    output.Notice{Kind: output.NoticeDailyCap, Title: "Daily cap reached", Text: "Route payments has used all of today's $40.00 cap."},
}

// TestSendNoticeOnce sends the same notice from several transactions at
// once and checks that exactly one of them enqueues it.
func TestSendNoticeOnce(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		sent  int
		start = make(chan struct{})
	)

	for range concurrentSenders {
		wg.Go(func() {
			<-start

			err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
				ok, err := SendNotice(t.Context(), tx, testNotice)
				if ok {
					mu.Lock()
					sent++
					mu.Unlock()
				}

				return err
			})
			require.NoError(t, err)
		})
	}

	close(start)
	wg.Wait()

	require.Equal(t, 1, sent, "exactly one sender claims the notice")

	jobs := collect[noticeJob](t, pool, "SELECT kind, payload FROM jobs WHERE kind = 'notice'")
	require.Len(t, jobs, 1)
	require.JSONEq(t, `{"sink": "slack-thread", "notice": {"kind": "daily_cap", "title": "Daily cap reached",
		"text": "Route payments has used all of today's $40.00 cap."}}`, string(jobs[0].Payload))
}

// TestSendNoticeWindow checks that a notice is sent again only once its
// window has passed.
func TestSendNoticeWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// later is when the second notice is sent, and window how far back
		// its window reaches.
		later, window time.Duration
		want          bool
	}{
		{name: "inside the window", later: 30 * time.Minute, window: time.Hour, want: false},
		{name: "at the start of the window", later: time.Hour, window: time.Hour, want: false},
		{name: "after the window", later: time.Hour + time.Second, window: time.Hour, want: true},
		{name: "a new day", later: 10 * time.Hour, window: 10*time.Hour - time.Minute, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)

			ok, err := SendNotice(t.Context(), pool, testNotice)
			require.NoError(t, err)
			require.True(t, ok)

			again := testNotice
			again.At = noticeAt.Add(tt.later)
			again.NotBefore = again.At.Add(-tt.window)

			ok, err = SendNotice(t.Context(), pool, again)
			require.NoError(t, err)
			require.Equal(t, tt.want, ok)

			other := testNotice
			other.Key = "daily_cap:route:checkout"

			ok, err = SendNotice(t.Context(), pool, other)
			require.NoError(t, err)
			require.True(t, ok, "another key has a window of its own")
		})
	}
}

// TestNoticeHandler runs notice jobs against the Slack output and other
// sinks.
func TestNoticeHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		// wantErr is nil for a job that succeeds.
		wantErr   error
		wantPosts int
	}{
		{
			name:      "the Slack output posts it in its channel",
			payload:   `{"sink": "slack-thread", "notice": {"kind": "daily_cap", "title": "Daily cap reached", "text": "Route payments has used all of today's $40.00 cap."}}`,
			wantPosts: 1,
		},
		{
			name:    "a sink that cannot take notices is skipped",
			payload: `{"sink": "plain", "notice": {"kind": "daily_cap", "title": "Daily cap reached", "text": "Text."}}`,
		},
		{
			name:    "a sink that is not configured gives up",
			payload: `{"sink": "gone", "notice": {"kind": "daily_cap", "title": "Daily cap reached", "text": "Text."}}`,
			wantErr: queue.ErrGiveUp,
		},
		{name: "a payload without a sink gives up", payload: `{"notice": {}}`, wantErr: queue.ErrGiveUp},
		{name: "a payload that does not decode gives up", payload: `[]`, wantErr: queue.ErrGiveUp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			h.handler.sinks["plain"] = output.NewMockSink(gomock.NewController(t))

			notices := NewNoticeHandler(Options{Sinks: h.handler.sinks})

			err := notices.Handle(t.Context(), db.Job{ID: 1, Kind: queue.KindNotice, Payload: []byte(tt.payload)})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			posts := h.slack.callsTo("chat.postMessage")
			require.Len(t, posts, tt.wantPosts)

			for _, p := range posts {
				require.Equal(t, "#alerts", p.Channel)
				require.Empty(t, p.ThreadTS)
				require.Equal(t, "Daily cap reached: Route payments has used all of today's $40.00 cap.", p.Text)
			}
		})
	}
}

// noticeJob is a notice job as the tests check it.
type noticeJob struct {
	Kind    string
	Payload []byte
}

package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/time/rate"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
)

// The Web API paths of the fake Slack server.
const (
	pathPost      = "/api/chat.postMessage"
	pathUpdate    = "/api/chat.update"
	pathPermalink = "/api/chat.getPermalink"
)

// The main message the fake Slack server posts, and its link.
const (
	mainChannel = "C123"
	mainTS      = "1793023321.000100"
	permalinkOK = "https://example.slack.com/archives/C123/p1793023321000100"
)

// botToken is the secret reference of the bot token in the sink tests.
var botToken = registry.SecretRef{Name: "sidekick-slack", Key: "botToken"}

func TestPublishFirstPost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		slack        *event.SlackRef
		wantChannel  string
		wantThreadTS string
		wantRefTS    string
	}{
		{name: "a webhook event posts in the channel", wantChannel: "#alerts", wantRefTS: mainTS},
		{
			name:         "a Slack message gets a reply in its thread",
			slack:        &event.SlackRef{Channel: "C999", TS: "1793000000.000001"},
			wantChannel:  "C999",
			wantThreadTS: "1793000000.000001",
			wantRefTS:    "1793000000.000001",
		},
		{
			name:         "a Slack message in a thread gets a reply in that thread",
			slack:        &event.SlackRef{Channel: "C999", TS: "1793000000.000005", ThreadTS: "1793000000.000001"},
			wantChannel:  "C999",
			wantThreadTS: "1793000000.000001",
			wantRefTS:    "1793000000.000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathPost, postedResponse(mainTS))
			api.ExpectAllCalled()

			s, clock := testSink(t, api)

			u := queuedUpdate()
			u.Event.Slack = tt.slack

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)

			sent := decodeMessage(t, api.Requests()[0].Body)
			require.Equal(t, tt.wantChannel, sent.Channel)
			require.Equal(t, tt.wantThreadTS, sent.ThreadTS)
			require.Equal(t, "Looking into it: checkout-api p99 latency above 2s", sent.Text)
			require.Equal(t, colorRunning, sent.Attachments[0].Color)

			require.Equal(t, output.Ref{
				Sink:     "slack-thread",
				External: mainChannel + ":" + mainTS,
				Data: map[string]string{
					dataChannel:  mainChannel,
					dataTS:       mainTS,
					dataThreadTS: tt.wantRefTS,
					dataSeq:      "0",
					dataEditedAt: strconv.FormatInt(clock.Now().UnixMilli(), 10),
				},
			}, got)
		})
	}
}

func TestPublishEditsAtMostOnceEveryThreeSeconds(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathPost, postedResponse(mainTS))

	s, clock := testSink(t, api)
	edits := recordEdits(api, clock)

	start := clock.Now()
	ref, err := s.Publish(t.Context(), queuedUpdate())
	require.NoError(t, err)

	for seq := int64(1); seq <= 3; seq++ {
		u := runningUpdate(int(seq))
		u.Seq, u.Previous = seq, &ref

		ref, err = s.Publish(t.Context(), u)
		require.NoError(t, err)
	}

	times := edits.times()
	require.Len(t, times, 3)
	require.Equal(t, []time.Duration{editInterval, editInterval, editInterval}, clock.waited())

	last := start
	for _, at := range times {
		require.GreaterOrEqual(t, at.Sub(last), editInterval, "edits are at least 3 seconds apart")
		last = at
	}

	require.Equal(t, "3", ref.Data[dataSeq])
	require.Equal(t, strconv.FormatInt(times[2].UnixMilli(), 10), ref.Data[dataEditedAt])
}

func TestPublishDoesNotWaitAfterAQuietSpell(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathPost, postedResponse(mainTS))
	api.Handle(http.MethodPost, pathUpdate, postedResponse(mainTS))
	api.ExpectAllCalled()

	s, clock := testSink(t, api)

	ref, err := s.Publish(t.Context(), queuedUpdate())
	require.NoError(t, err)

	clock.advance(10 * time.Second)

	u := runningUpdate(1)
	u.Seq, u.Previous = 1, &ref

	_, err = s.Publish(t.Context(), u)
	require.NoError(t, err)
	require.Empty(t, clock.waited())
}

func TestPublishDropsAnOlderSeq(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		seq  int64
	}{
		{name: "the same seq", seq: 5},
		{name: "an older seq", seq: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s, _ := testSink(t, api)

			prev := shownRef(5, "")
			u := runningUpdate(2)
			u.Seq, u.Previous = tt.seq, &prev

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)
			require.Equal(t, prev, got)
			require.Empty(t, api.Requests(), "nothing is sent to Slack")
		})
	}
}

func TestPublishFinal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report *report.Report
		// announced is what the stored ref says was announced already.
		announced string
		// wantReplies are the notification texts of the thread replies, in
		// order.
		wantReplies   []string
		wantAnnounced string
	}{
		{
			name:          "a verdict is announced in a new thread reply",
			report:        verdictReport(report.TruePositive),
			wantReplies:   []string{"Real issue (high confidence): checkout-api p99 latency above 2s"},
			wantAnnounced: string(report.TruePositive),
		},
		{
			name:   "a long report continues in the thread before the reply",
			report: longReport(),
			wantReplies: []string{
				"More of the report: checkout-api p99 latency above 2s",
				"Real issue (high confidence): checkout-api p99 latency above 2s",
			},
			wantAnnounced: string(report.TruePositive),
		},
		{
			name:          "Not sure yet is announced too",
			report:        verdictReport(report.Inconclusive),
			wantReplies:   []string{"Not sure yet: checkout-api p99 latency above 2s"},
			wantAnnounced: string(report.Inconclusive),
		},
		{
			name:          "a run that could not finish is announced",
			report:        &report.Report{StopReason: report.StopError, StopDetail: "The model returned an error."},
			wantReplies:   []string{"Could not finish: checkout-api p99 latency above 2s"},
			wantAnnounced: announcedFailed,
		},
		{
			name:          "a retried final with the same verdict posts no second reply",
			report:        verdictReport(report.TruePositive),
			announced:     string(report.TruePositive),
			wantAnnounced: string(report.TruePositive),
		},
		{
			name:          "a new verdict is announced again",
			report:        verdictReport(report.FalsePositive),
			announced:     string(report.TruePositive),
			wantReplies:   []string{"False alarm (high confidence): checkout-api p99 latency above 2s"},
			wantAnnounced: string(report.FalsePositive),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, pathUpdate, postedResponse(mainTS))

			if len(tt.wantReplies) > 0 {
				api.Handle(http.MethodGet, pathPermalink, fakehttp.Response{Body: []byte(`{"ok":true,"permalink":"` + permalinkOK + `"}`)})

				for i := range tt.wantReplies {
					api.Handle(http.MethodPost, pathPost, postedResponse("1793023399.00000"+strconv.Itoa(i+1)))
				}
			}

			api.ExpectAllCalled()

			s, _ := testSink(t, api)

			prev := shownRef(1, tt.announced)
			u := testUpdate(output.PhaseFinal, tt.report)
			u.Seq, u.Previous = 2, &prev

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)
			require.Equal(t, tt.wantAnnounced, got.Data[dataAnnounced])
			require.Equal(t, "2", got.Data[dataSeq])

			var replies []string

			for _, req := range api.Requests() {
				switch req.Path {
				case pathUpdate:
					edit := decodeMessage(t, req.Body)
					require.Equal(t, mainChannel, edit.Channel)
					require.Equal(t, mainTS, edit.TS)
				case pathPost:
					reply := decodeMessage(t, req.Body)
					require.Equal(t, mainChannel, reply.Channel)
					require.Equal(t, mainTS, reply.ThreadTS, "replies go in the thread of the main message")
					replies = append(replies, reply.Text)
				}
			}

			require.Equal(t, tt.wantReplies, replies)

			if len(tt.wantReplies) > 0 {
				last := decodeMessage(t, api.Requests()[len(api.Requests())-1].Body)
				require.Contains(t, last.Attachments[0].Blocks[0].Text.Text, "<"+permalinkOK+"|See the full report>")
			}
		})
	}
}

func TestPublishFinalOnTheFirstPost(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathPost, postedResponse(mainTS), postedResponse("1793023399.000001"))
	api.Handle(http.MethodGet, pathPermalink, fakehttp.Response{Body: []byte(`{"ok":true,"permalink":"` + permalinkOK + `"}`)})
	api.ExpectAllCalled()

	s, _ := testSink(t, api)

	got, err := s.Publish(t.Context(), testUpdate(output.PhaseFinal, verdictReport(report.Flapping)))
	require.NoError(t, err)
	require.Equal(t, string(report.Flapping), got.Data[dataAnnounced])

	reply := decodeMessage(t, api.Requests()[2].Body)
	require.Equal(t, mainTS, reply.ThreadTS)
}

func TestPublishRetriesAfterRateLimit(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathUpdate,
		fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "2"}},
		postedResponse(mainTS),
	)
	api.ExpectAllCalled()

	s, clock := testSink(t, api)

	prev := shownRef(1, "")
	u := runningUpdate(2)
	u.Seq, u.Previous = 2, &prev

	got, err := s.Publish(t.Context(), u)
	require.NoError(t, err)
	require.Equal(t, "2", got.Data[dataSeq])
	require.Equal(t, []time.Duration{2 * time.Second}, clock.waited())
	require.Len(t, api.Requests(), 2)
}

func TestPublishPostsAgainWhenTheMessageWasDeleted(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathUpdate, fakehttp.Response{Body: []byte(`{"ok":false,"error":"message_not_found"}`)})
	api.Handle(http.MethodPost, pathPost, postedResponse("1793023500.000100"))
	api.ExpectAllCalled()

	s, _ := testSink(t, api)

	prev := shownRef(1, "")
	u := runningUpdate(2)
	u.Seq, u.Previous = 2, &prev

	got, err := s.Publish(t.Context(), u)
	require.NoError(t, err)
	require.Equal(t, mainChannel+":1793023500.000100", got.External)
}

func TestPublishRepliesWithoutAPermalink(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathUpdate, postedResponse(mainTS))
	api.Handle(http.MethodGet, pathPermalink, fakehttp.Response{Body: []byte(`{"ok":false,"error":"internal_error"}`)})
	api.Handle(http.MethodPost, pathPost, postedResponse("1793023399.000001"))
	api.ExpectAllCalled()

	s, _ := testSink(t, api)

	prev := shownRef(1, "")
	u := testUpdate(output.PhaseFinal, verdictReport(report.Duplicate))
	u.Seq, u.Previous = 2, &prev

	_, err := s.Publish(t.Context(), u)
	require.NoError(t, err)

	reply := decodeMessage(t, api.Requests()[2].Body)
	require.Equal(t, ":link: *Duplicate* · High confidence", reply.Attachments[0].Blocks[0].Text.Text)
}

func TestPublishFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		phase   output.Phase
		prev    bool
		wantErr string
	}{
		{name: "the post fails", path: pathPost, phase: output.PhaseQueued, wantErr: "post the message: post message: call chat.postMessage: Slack answered channel_not_found"},
		{name: "the edit fails", path: pathUpdate, phase: output.PhaseRunning, prev: true, wantErr: "edit the message: update message: call chat.update: Slack answered channel_not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, tt.path, fakehttp.Response{Body: []byte(`{"ok":false,"error":"channel_not_found"}`)})
			api.ExpectAllCalled()

			s, _ := testSink(t, api)

			u := testUpdate(tt.phase, nil)
			if tt.prev {
				prev := shownRef(0, "")
				u.Previous = &prev
			}

			_, err := s.Publish(t.Context(), u)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestPublishWatcherChange(t *testing.T) {
	t.Parallel()

	recovered := testWatchers()[1]
	shown := shownRef(5, string(report.TruePositive))

	tests := []struct {
		name     string
		previous *output.Ref
		watcher  *output.WatcherState
		// wantPaths are the Web API calls, in order.
		wantPaths []string
		wantRef   output.Ref
	}{
		{
			name:      "a change is a reply in the thread, whatever the Seq the message shows",
			previous:  &shown,
			watcher:   &recovered,
			wantPaths: []string{pathPermalink, pathPost},
			wantRef:   shown,
		},
		{
			name:      "without a message, the message is posted first",
			watcher:   &recovered,
			wantPaths: []string{pathPost, pathPermalink, pathPost},
		},
		{
			name:     "an update that does not say which watcher changed posts nothing",
			previous: &shown,
			wantRef:  shown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			if len(tt.wantPaths) > 0 {
				posts := []fakehttp.Response{postedResponse("1793023399.000001")}
				if tt.previous == nil {
					posts = []fakehttp.Response{postedResponse(mainTS), postedResponse("1793023399.000001")}
				}

				api.Handle(http.MethodPost, pathPost, posts...)
				api.Handle(http.MethodGet, pathPermalink, fakehttp.Response{Body: []byte(`{"ok":true,"permalink":"` + permalinkOK + `"}`)})
			}

			api.ExpectAllCalled()

			s, clock := testSink(t, api)

			u := testUpdate(output.PhaseWatcher, nil)
			u.Seq, u.Previous, u.Watcher = 3, tt.previous, tt.watcher
			u.Watchers = testWatchers()

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)

			var paths []string
			for _, req := range api.Requests() {
				paths = append(paths, req.Path)
			}

			require.Equal(t, tt.wantPaths, paths)

			if tt.watcher == nil {
				require.Equal(t, tt.wantRef, got)

				return
			}

			reply := decodeMessage(t, api.Requests()[len(api.Requests())-1].Body)
			require.Equal(t, mainChannel, reply.Channel)
			require.Equal(t, mainTS, reply.ThreadTS, "the reply goes in the thread of the main message")
			require.Equal(t, "Back up alerts-cleared: checkout-api p99 latency above 2s", reply.Text)
			require.Contains(t, reply.Attachments[0].Blocks[1].Elements[0].Text, "<"+permalinkOK+"|See the investigation>")

			if tt.previous != nil {
				require.Equal(t, tt.wantRef, got, "the ref of the message does not change")

				return
			}

			main := decodeMessage(t, api.Requests()[0].Body)
			require.Equal(t, "#alerts", main.Channel)
			require.Equal(t, "Looking into it: checkout-api p99 latency above 2s", main.Text)
			require.Equal(t, output.Ref{
				Sink:     "slack-thread",
				External: mainChannel + ":" + mainTS,
				Data: map[string]string{
					dataChannel:  mainChannel,
					dataTS:       mainTS,
					dataThreadTS: mainTS,
					dataSeq:      "3",
					dataEditedAt: strconv.FormatInt(clock.Now().UnixMilli(), 10),
				},
			}, got, "the state that follows has a higher Seq, so it edits the message")
		})
	}
}

func TestAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase output.Phase
		want  bool
	}{
		{phase: output.PhaseQueued, want: true},
		{phase: output.PhaseRunning, want: true},
		{phase: output.PhaseFinal, want: true},
		{phase: output.PhaseFollowUp},
		{phase: output.PhaseProposal},
		{phase: output.PhaseResolved},
		{phase: output.PhaseCancelled},
		{phase: output.PhaseWatcher, want: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, (&Sink{}).Accepts(tt.phase))
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{name: "a channel name", cfg: Config{Channel: "#alerts"}},
		{name: "a channel ID", cfg: Config{Channel: "C0123ALERTS"}},
		{name: "no channel", wantErr: errNoChannel},
		{name: "a blank channel", cfg: Config{Channel: "  "}, wantErr: errNoChannel},
		{name: "a GovSlack API URL", cfg: Config{Channel: "#alerts", APIURL: "https://slack-gov.com/api/"}},
		{name: "an API URL without the slash", cfg: Config{Channel: "#alerts", APIURL: "https://slack-gov.com/api"}, wantErr: errBadAPIURL},
		{name: "an API URL that is not http", cfg: Config{Channel: "#alerts", APIURL: "ftp://slack.example.com/api/"}, wantErr: errBadAPIURL},
		{name: "both wrong", cfg: Config{APIURL: "slack.com/api/"}, wantErr: errBadAPIURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.cfg.Validate(), tt.wantErr)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deps    registry.Deps
		wantErr error
	}{
		{name: "with the token", deps: registry.Deps{SlackBotToken: botToken, Secrets: staticSecrets{}}},
		{name: "without the slack section", deps: registry.Deps{Secrets: staticSecrets{}}, wantErr: errNoToken},
		{name: "without a secrets resolver", deps: registry.Deps{SlackBotToken: botToken}, wantErr: errNoSecrets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := New(t.Context(), "slack-thread", Config{Channel: "#alerts"}, tt.deps)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantErr == nil {
				require.NotNil(t, s)
			}
		})
	}
}

func TestNewUsesTheAPIURLAndClient(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, "/slack/api/chat.postMessage", postedResponse(mainTS))
	api.ExpectAllCalled()

	s, err := New(t.Context(), "slack-thread", Config{Channel: "#alerts", APIURL: api.URL() + "/slack/api/"}, registry.Deps{
		SlackBotToken: botToken, Secrets: staticSecrets{}, HTTP: api.Client(),
	})
	require.NoError(t, err)

	_, err = s.Publish(t.Context(), queuedUpdate())
	require.NoError(t, err)
}

func TestTheTokenIsReadOnEveryCall(t *testing.T) {
	t.Parallel()

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	gomock.InOrder(
		secrets.EXPECT().Resolve(gomock.Any(), botToken).Return([]byte("first-value\n"), nil),
		secrets.EXPECT().Resolve(gomock.Any(), botToken).Return([]byte("rotated-value\n"), nil),
	)

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathPost, postedResponse(mainTS))
	api.Handle(http.MethodPost, pathUpdate, postedResponse(mainTS))
	api.ExpectAllCalled()

	s, _ := testSinkWith(t, api, secrets)

	ref, err := s.Publish(t.Context(), queuedUpdate())
	require.NoError(t, err)

	u := runningUpdate(1)
	u.Seq, u.Previous = 2, &ref

	_, err = s.Publish(t.Context(), u)
	require.NoError(t, err)

	requests := api.Requests()
	require.Equal(t, "Bearer first-value", requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer rotated-value", requests[1].Header.Get("Authorization"))
}

// fakeClock is a clock whose waits return at once and move it forward.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// Now returns the clock's time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Wait records d and moves the clock forward by it.
func (c *fakeClock) Wait(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)

	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

func (c *fakeClock) waited() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.waits
}

// editLog records when the fake Slack server got each chat.update.
type editLog struct {
	mu sync.Mutex
	at []time.Time
}

func (l *editLog) times() []time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.at
}

// recordEdits answers chat.update on api and records the clock's time of
// each call.
func recordEdits(api *fakehttp.Server, clock *fakeClock) *editLog {
	l := &editLog{}

	api.HandleFunc(http.MethodPost, pathUpdate, func(w http.ResponseWriter, _ *http.Request) {
		l.mu.Lock()
		l.at = append(l.at, clock.Now())
		l.mu.Unlock()

		_, _ = w.Write([]byte(`{"ok":true,"channel":"` + mainChannel + `","ts":"` + mainTS + `"}`))
	})

	return l
}

// staticSecrets resolves every reference to the same token.
type staticSecrets struct{}

func (staticSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte(testToken + "\n"), nil
}

// testSink returns a sink for api with a fake clock and no rate limits.
func testSink(t *testing.T, api *fakehttp.Server) (*Sink, *fakeClock) {
	t.Helper()

	return testSinkWith(t, api, staticSecrets{})
}

// testSinkWith is testSink with the given secrets resolver.
func testSinkWith(t *testing.T, api *fakehttp.Server, secrets registry.Secrets) (*Sink, *fakeClock) {
	t.Helper()

	s, err := newSink("slack-thread", Config{Channel: "#alerts"}, registry.Deps{
		Secrets:       secrets,
		HTTP:          api.Client(),
		SlackBotToken: botToken,
	}, api.URL()+"/api/")
	require.NoError(t, err)

	clock := &fakeClock{now: time.Date(2026, 10, 26, 14, 2, 1, 0, time.UTC)}
	s.now, s.wait = clock.Now, clock.Wait
	s.client.wait = clock.Wait
	s.client.channelLimit = rate.Inf

	return s, clock
}

// queuedUpdate is the first update of an investigation, with Seq 0.
func queuedUpdate() output.Update {
	u := testUpdate(output.PhaseQueued, nil)
	u.Seq = 0

	return u
}

// shownRef is the ref of the main message showing seq, edited long ago,
// after announcing announced.
func shownRef(seq int64, announced string) output.Ref {
	data := map[string]string{
		dataChannel:  mainChannel,
		dataTS:       mainTS,
		dataThreadTS: mainTS,
		dataSeq:      strconv.FormatInt(seq, 10),
		dataEditedAt: "0",
	}

	if announced != "" {
		data[dataAnnounced] = announced
	}

	return output.Ref{Sink: "slack-thread", External: mainChannel + ":" + mainTS, Data: data}
}

// postedResponse is Slack's answer to a post or edit of the message at ts.
func postedResponse(ts string) fakehttp.Response {
	return fakehttp.Response{Body: []byte(`{"ok":true,"channel":"` + mainChannel + `","ts":"` + ts + `"}`)}
}

// decodeMessage decodes a request body sent to chat.postMessage or
// chat.update.
func decodeMessage(t *testing.T, body []byte) message {
	t.Helper()

	var m message
	require.NoError(t, json.Unmarshal(body, &m))
	require.NotContains(t, m.Text, "<!", "the notification text has no mention")

	return m
}

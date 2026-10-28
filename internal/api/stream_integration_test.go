//go:build integration

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
)

const (
	// streamTimeout is how long a test waits for the next event.
	streamTimeout = 10 * time.Second
	// quietTime is how long a test waits to see that no event comes.
	quietTime = 300 * time.Millisecond
	// fastHeartbeat makes a stream read the database often.
	fastHeartbeat = 100 * time.Millisecond
)

// sseEvent is one event of a stream, or a comment line.
type sseEvent struct {
	ID      string
	Name    string
	Data    string
	Comment bool
}

// sseStream reads the events of an open stream.
type sseStream struct {
	status int
	header http.Header
	events chan sseEvent
	cancel context.CancelFunc
}

// serve starts a real HTTP server on the instance's handler, since a stream
// needs a connection that stays open.
func (s server) serve(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	return srv
}

// openStream opens the stream of an investigation as the session, with the
// headers given, and starts reading its events.
func openStream(t *testing.T, srv *httptest.Server, path, session string, headers map[string]string) *sseStream {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)

	if session != "" {
		req.AddCookie(newSessionCookie(session))
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := srv.Client().Do(req) //nolint:bodyclose // The goroutine below reads the stream and closes the body.
	require.NoError(t, err)

	stream := &sseStream{status: resp.StatusCode, header: resp.Header, events: make(chan sseEvent, 1000), cancel: cancel}

	go func() {
		defer close(stream.events)
		defer func() { _ = resp.Body.Close() }()

		var ev sseEvent

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(nil, 1<<20)

		for scanner.Scan() {
			line := scanner.Text()

			switch {
			case line == "":
				if ev.Name != "" {
					stream.events <- ev
				}

				ev = sseEvent{}
			case strings.HasPrefix(line, ":"):
				stream.events <- sseEvent{Comment: true, Data: strings.TrimSpace(strings.TrimPrefix(line, ":"))}
			case strings.HasPrefix(line, "id: "):
				ev.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()

	return stream
}

// next returns the next event or comment, and fails when none comes or the
// stream has ended.
func (s *sseStream) next(t *testing.T) sseEvent {
	t.Helper()

	select {
	case ev, ok := <-s.events:
		require.True(t, ok, "the stream ended")

		return ev
	case <-time.After(streamTimeout):
		require.FailNow(t, "no event came")
	}

	return sseEvent{}
}

// nextEvent is next, skipping comments.
func (s *sseStream) nextEvent(t *testing.T) sseEvent {
	t.Helper()

	for {
		if ev := s.next(t); !ev.Comment {
			return ev
		}
	}
}

// requireEnded checks that the stream ends, without an event first.
func (s *sseStream) requireEnded(t *testing.T) {
	t.Helper()

	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}

			require.True(t, ev.Comment, "an event came: %+v", ev)
		case <-time.After(streamTimeout):
			require.FailNow(t, "the stream did not end")
		}
	}
}

// requireQuiet checks that no event comes for a while.
func (s *sseStream) requireQuiet(t *testing.T) {
	t.Helper()

	select {
	case ev, ok := <-s.events:
		require.True(t, ok, "the stream ended")
		require.True(t, ev.Comment, "an event came: %+v", ev)
	case <-time.After(quietTime):
	}
}

// requireStep checks that the next event is the step with seq of kind.
func (s *sseStream) requireStep(t *testing.T, seq, kind string) {
	t.Helper()

	ev := s.nextEvent(t)
	require.Equal(t, "step", ev.Name)
	require.Equal(t, seq, ev.ID)

	var step struct {
		Seq  int    `json:"seq"`
		Kind string `json:"kind"`
	}

	require.NoError(t, json.Unmarshal([]byte(ev.Data), &step))
	require.Equal(t, kind, step.Kind)
}

// requireStatus checks that the next event is a status event of status, and
// returns its data.
func (s *sseStream) requireStatus(t *testing.T, status string) map[string]any {
	t.Helper()

	ev := s.nextEvent(t)
	require.Equal(t, "status", ev.Name)
	require.Empty(t, ev.ID, "a status event has no id, so the last step stays the one to resume from")

	var data map[string]any

	require.NoError(t, json.Unmarshal([]byte(ev.Data), &data))
	require.Equal(t, status, data["status"])

	return data
}

const streamPath = "/api/v1/investigations/" + inv3 + "/stream"

// addStep saves a step of inv3 in a transaction, as the replica that runs
// the investigation does.
func addStep(t *testing.T, s server, seq int, kind string) {
	t.Helper()

	_, err := s.pool.Exec(t.Context(), `INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
		VALUES ($1, $2, 2, $3, '[{"type": "text", "text": "more"}]', 'ok', now(), 1)`, inv3, seq, kind)
	require.NoError(t, err)
}

func TestStreamSendsTheStepsThenTheStatus(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	stream := openStream(t, s.serve(t), streamPath, viSession, nil)

	require.Equal(t, http.StatusOK, stream.status)
	require.Equal(t, "text/event-stream", stream.header.Get("Content-Type"))
	require.Equal(t, "no-store", stream.header.Get("Cache-Control"))

	for seq, kind := range []string{"prompt", "model_turn", "tool_result", "note", "model_turn"} {
		stream.requireStep(t, strconv.Itoa(seq), kind)
	}

	status := stream.requireStatus(t, "running")
	require.EqualValues(t, 2, status["run"])
	confirmed, ok := status["confirmed"].(bool)
	require.True(t, ok)
	require.False(t, confirmed)
	require.EqualValues(t, 500, status["costMicros"])
	stream.requireQuiet(t)
}

func TestStreamResumes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		want    []string
	}{
		{name: "after Last-Event-ID", path: streamPath, headers: map[string]string{"Last-Event-ID": "2"}, want: []string{"3", "4"}},
		{name: "after the after parameter", path: streamPath + "?after=3", want: []string{"4"}},
		{name: "Last-Event-ID wins", path: streamPath + "?after=0", headers: map[string]string{"Last-Event-ID": "3"}, want: []string{"4"}},
		{name: "the last step has been seen", path: streamPath, headers: map[string]string{"Last-Event-ID": "4"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupInvestigations(t)
			stream := openStream(t, s.serve(t), tt.path, viSession, tt.headers)

			for _, seq := range tt.want {
				ev := stream.nextEvent(t)
				require.Equal(t, "step", ev.Name)
				require.Equal(t, seq, ev.ID)
			}

			stream.requireStatus(t, "running")
		})
	}
}

// TestStreamOnAReplicaThatDoesNotRunTheInvestigation opens streams on two
// instances on one database and changes the investigation the way the
// instance that runs it does, with writes to the database. Both streams
// get the changes.
func TestStreamOnAReplicaThatDoesNotRunTheInvestigation(t *testing.T) {
	t.Parallel()

	first := setupInvestigations(t)
	second := first.replica(t, nil)

	streams := []*sseStream{
		openStream(t, first.serve(t), streamPath+"?after=4", viSession, nil),
		openStream(t, second.serve(t), streamPath+"?after=4", viSession, nil),
	}

	for _, stream := range streams {
		stream.requireStatus(t, "running")
	}

	addStep(t, first, 5, "model_turn")

	for _, stream := range streams {
		stream.requireStep(t, "5", "model_turn")
	}

	_, err := first.pool.Exec(t.Context(), "UPDATE investigations SET status = 'final', verdict = 'true_positive', confidence = 'high' WHERE id = $1", inv3)
	require.NoError(t, err)

	for _, stream := range streams {
		status := stream.requireStatus(t, "final")
		require.Equal(t, "true_positive", status["verdict"])
		require.Equal(t, "high", status["confidence"])
	}

	for _, stream := range streams {
		stream.requireQuiet(t)
	}
}

func TestStreamSendsEachStepOnce(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	const burst = 250

	_, err := s.pool.Exec(t.Context(), `INSERT INTO steps (investigation_id, seq, run, kind, parts, status, started_at, duration_ms)
		SELECT $1, n, 2, 'note', '[]', 'ok', now(), 1 FROM generate_series(5, $2::int) AS n`, inv3, 4+burst)
	require.NoError(t, err)

	for seq := 5; seq < 5+burst; seq++ {
		ev := stream.nextEvent(t)
		require.Equal(t, "step", ev.Name)
		require.Equal(t, strconv.Itoa(seq), ev.ID, "in order, once each, in more than one batch")
	}

	stream.requireQuiet(t)
}

func TestStreamSendsWatcherChanges(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	_, err := s.pool.Exec(t.Context(), "UPDATE watchers SET checks = checks + 1, last_check_at = now() WHERE id = '0192d3a4-0000-7000-8000-0000000000d2'")
	require.NoError(t, err)

	ev := stream.nextEvent(t)
	require.Equal(t, "watchers", ev.Name)
	require.JSONEq(t, "{}", ev.Data)
}

func TestStreamSendsAStopRequest(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)

	first := stream.requireStatus(t, "running")
	require.NotContains(t, first, "cancelRequestedAt")

	_, err := s.pool.Exec(t.Context(), "UPDATE investigations SET cancel_requested_at = now() WHERE id = $1", inv3)
	require.NoError(t, err)

	require.Contains(t, stream.requireStatus(t, "running"), "cancelRequestedAt")
}

func TestStreamHeartbeat(t *testing.T) {
	t.Parallel()

	s := setupInvestigationsWith(t, func(o *api.Options) { o.StreamHeartbeat = fastHeartbeat })
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	ev := stream.next(t)
	require.True(t, ev.Comment)
	require.Equal(t, "heartbeat", ev.Data)
}

// TestStreamReadsWhatANotificationMissed saves a step with the trigger
// that notifies off, so no notification comes, and checks that the next
// heartbeat finds it.
func TestStreamReadsWhatANotificationMissed(t *testing.T) {
	t.Parallel()

	s := setupInvestigationsWith(t, func(o *api.Options) { o.StreamHeartbeat = fastHeartbeat })
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	_, err := s.pool.Exec(t.Context(), "ALTER TABLE steps DISABLE TRIGGER steps_notify")
	require.NoError(t, err)

	addStep(t, s, 5, "model_turn")

	_, err = s.pool.Exec(t.Context(), "ALTER TABLE steps ENABLE TRIGGER steps_notify")
	require.NoError(t, err)

	stream.requireStep(t, "5", "model_turn")
}

func TestStreamEndsAfterItsLifetime(t *testing.T) {
	t.Parallel()

	s := setupInvestigationsWith(t, func(o *api.Options) { o.StreamLifetime = 300 * time.Millisecond })
	stream := openStream(t, s.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")
	stream.requireEnded(t)

	again := openStream(t, s.serve(t), streamPath, viSession, map[string]string{"Last-Event-ID": "4"})
	again.requireStatus(t, "running")
}

func TestStreamEndsWhenTheHubStops(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	hub, _ := startHub(t, ctx, s.pool, api.HubOptions{})
	replica := s.replica(t, func(o *api.Options) { o.Hub = hub })
	stream := openStream(t, replica.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	stop()
	stream.requireEnded(t)
}

// TestStreamListensAgainAfterTheConnectionFails ends the hub's listening
// connection, saves a step while the hub waits to listen again, and checks
// that the stream gets the step once it does, and the one after it.
func TestStreamListensAgainAfterTheConnectionFails(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	const retry = time.Second

	hub, pid := startHub(t, t.Context(), s.pool, api.HubOptions{ListenRetry: retry})
	replica := s.replica(t, func(o *api.Options) { o.Hub = hub })
	stream := openStream(t, replica.serve(t), streamPath+"?after=4", viSession, nil)
	stream.requireStatus(t, "running")

	var ended bool

	require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT pg_terminate_backend($1)", pid).Scan(&ended))
	require.True(t, ended)

	addStep(t, s, 5, "model_turn")
	stream.requireStep(t, "5", "model_turn")

	// The hub says everything may have changed when it listens again, so
	// the watchers are read again too.
	require.Equal(t, "watchers", stream.nextEvent(t).Name)

	addStep(t, s, 6, "note")
	stream.requireStep(t, "6", "note")
}

func TestStreamLimit(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)

	hub, _ := startHub(t, t.Context(), s.pool, api.HubOptions{MaxStreams: 2})
	replica := s.replica(t, func(o *api.Options) { o.Hub = hub })
	srv := replica.serve(t)

	first := openStream(t, srv, streamPath+"?after=4", viSession, nil)
	second := openStream(t, srv, streamPath+"?after=4", viSession, nil)
	first.requireStatus(t, "running")
	second.requireStatus(t, "running")

	third := openStream(t, srv, streamPath, viSession, nil)
	require.Equal(t, http.StatusTooManyRequests, third.status)
	require.Equal(t, "application/problem+json", third.header.Get("Content-Type"))

	first.cancel()

	require.Eventually(t, func() bool {
		again := openStream(t, srv, streamPath+"?after=4", viSession, nil)
		defer again.cancel()

		return again.status == http.StatusOK
	}, testWait, testPoll, "a place frees up when a stream ends")
}

func TestStreamRefusals(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	srv := s.serve(t)

	tests := []struct {
		name       string
		path       string
		session    string
		headers    map[string]string
		wantStatus int
	}{
		{name: "no session", path: streamPath, wantStatus: http.StatusUnauthorized},
		{name: "an expired session", path: streamPath, session: viExpiredSession, wantStatus: http.StatusUnauthorized},
		{name: "no such investigation", path: "/api/v1/investigations/0192d3a4-0000-7000-8000-0000000000ff/stream", session: viSession, wantStatus: http.StatusNotFound},
		{name: "an ID that is not a UUID", path: "/api/v1/investigations/one/stream", session: viSession, wantStatus: http.StatusBadRequest},
		{name: "a Last-Event-ID that is not a seq", path: streamPath, session: viSession, headers: map[string]string{"Last-Event-ID": "abc"}, wantStatus: http.StatusBadRequest},
		{name: "a Last-Event-ID below the first step", path: streamPath, session: viSession, headers: map[string]string{"Last-Event-ID": "-2"}, wantStatus: http.StatusBadRequest},
		{name: "an after that is not a number", path: streamPath + "?after=x", session: viSession, wantStatus: http.StatusBadRequest},
		{name: "an after below the first step", path: streamPath + "?after=-2", session: viSession, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stream := openStream(t, srv, tt.path, tt.session, tt.headers)
			require.Equal(t, tt.wantStatus, stream.status)
			require.Equal(t, "application/problem+json", stream.header.Get("Content-Type"))
		})
	}
}

func TestStreamWithAToken(t *testing.T) {
	t.Parallel()

	s := setupInvestigations(t)
	srv := s.serve(t)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+streamPath+"?after=4", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer skt_viewer")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
}

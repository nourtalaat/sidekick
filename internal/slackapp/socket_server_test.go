//go:build integration

package slackapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const (
	// socketPath is where the fake Socket Mode server takes connections.
	socketPath = "/socket"
	// pingEvery is how often the fake server pings each connection, as
	// Slack does.
	pingEvery = 500 * time.Millisecond
	// writeTimeout bounds one write to a connection.
	writeTimeout = 5 * time.Second
)

// fakeSocket is a Socket Mode server for one test, on the test's fake
// Slack Web API: apps.connections.open hands out its URL, and every
// connection gets a hello. It records the acknowledgements it reads, with
// their payloads.
type fakeSocket struct {
	t        *testing.T
	upgrader websocket.Upgrader
	// onAck runs as each acknowledgement is read, before it is recorded.
	onAck func(envelopeID string)

	mu      sync.Mutex
	conns   []*socketConn
	open    int
	maxOpen int
	opened  int
	acks    []string
	// payloads holds the payload of each acknowledgement that had one.
	payloads map[string]json.RawMessage
}

// socketConn is one connection with its write lock, since a connection
// takes one writer at a time.
type socketConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

// newFakeSocket serves Socket Mode on slack.
func newFakeSocket(t *testing.T, slack *fakehttp.Server) *fakeSocket {
	t.Helper()

	// The client sends Slack's own Origin, which a fake server cannot match.
	f := &fakeSocket{
		t:        t,
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		payloads: map[string]json.RawMessage{},
	}
	wsURL := "ws" + strings.TrimPrefix(slack.URL(), "http") + socketPath

	slack.HandleFunc(http.MethodPost, "/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xapp-test-app-token" {
			if _, err := fmt.Fprint(w, `{"ok": false, "error": "invalid_auth"}`); err != nil {
				t.Errorf("answer apps.connections.open: %v", err)
			}

			return
		}

		if _, err := fmt.Fprintf(w, `{"ok": true, "url": %q}`, wsURL); err != nil {
			t.Errorf("answer apps.connections.open: %v", err)
		}
	})

	slack.HandleFunc(http.MethodGet, socketPath, f.serve)

	return f
}

// send sends a Socket Mode events_api message with the payload to the
// newest open connection.
func (f *fakeSocket) send(envelopeID string, retry int, payload []byte) {
	f.t.Helper()

	f.sendRequest(socketmode.RequestTypeEventsAPI, envelopeID, retry, payload)
}

// sendRequest sends a Socket Mode message of kind, such as slash_commands,
// with the payload to the newest open connection.
func (f *fakeSocket) sendRequest(kind, envelopeID string, retry int, payload []byte) {
	f.t.Helper()

	msg, err := json.Marshal(map[string]any{
		"type":                     kind,
		"envelope_id":              envelopeID,
		"payload":                  json.RawMessage(payload),
		"accepts_response_payload": kind != socketmode.RequestTypeEventsAPI,
		"retry_attempt":            retry,
	})
	require.NoError(f.t, err)

	f.mu.Lock()
	c := f.conns[len(f.conns)-1]
	f.mu.Unlock()

	require.NoError(f.t, c.write(websocket.TextMessage, msg))
}

// acked reports whether the payload with envelopeID was acknowledged.
func (f *fakeSocket) acked(envelopeID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, id := range f.acks {
		if id == envelopeID {
			return true
		}
	}

	return false
}

// ackPayload returns the payload the acknowledgement of envelopeID carried,
// or nil for an empty ack.
func (f *fakeSocket) ackPayload(envelopeID string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.payloads[envelopeID]
}

// counts returns how many connections are open, the most that were open at
// once, and how many were opened in all.
func (f *fakeSocket) counts() (open, maxOpen, opened int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.open, f.maxOpen, f.opened
}

// serve upgrades one connection, says hello, pings it and reads its
// acknowledgements until it closes.
func (f *fakeSocket) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		f.t.Errorf("upgrade the Socket Mode connection: %v", err)

		return
	}

	c := &socketConn{ws: ws}

	f.mu.Lock()
	f.conns = append(f.conns, c)
	f.open++
	f.opened++
	f.maxOpen = max(f.maxOpen, f.open)
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.open--
		f.mu.Unlock()

		_ = ws.Close()
	}()

	if err := c.write(websocket.TextMessage, []byte(`{"type": "hello", "num_connections": 1, "connection_info": {"app_id": "A01SIDEKICK"}}`)); err != nil {
		return
	}

	stop := make(chan struct{})
	defer close(stop)

	go c.ping(stop)

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			return
		}

		var ack struct {
			EnvelopeID string          `json:"envelope_id"`
			Payload    json.RawMessage `json:"payload"`
		}

		if json.Unmarshal(msg, &ack) != nil || ack.EnvelopeID == "" {
			continue
		}

		if f.onAck != nil {
			f.onAck(ack.EnvelopeID)
		}

		f.mu.Lock()
		f.acks = append(f.acks, ack.EnvelopeID)
		if ack.Payload != nil {
			f.payloads[ack.EnvelopeID] = ack.Payload
		}

		f.mu.Unlock()
	}
}

// write writes one message under the connection's write lock.
func (c *socketConn) write(kind int, msg []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return fmt.Errorf("set the write deadline: %w", err)
	}

	if err := c.ws.WriteMessage(kind, msg); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}

// ping pings the connection every pingEvery until stop is closed.
func (c *socketConn) ping(stop <-chan struct{}) {
	ticker := time.NewTicker(pingEvery)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
			c.mu.Unlock()

			if err != nil {
				return
			}
		}
	}
}

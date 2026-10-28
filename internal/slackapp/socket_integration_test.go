//go:build integration

package slackapp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

const (
	// socketTimeout bounds how long a test waits for the connection to do
	// something.
	socketTimeout = 30 * time.Second
	// socketPoll is how often a test checks while it waits.
	socketPoll = 20 * time.Millisecond
	// socketQuiet is how long a test watches for something that must not
	// happen.
	socketQuiet = time.Second
	// testSlotWait replaces the slot and reconnect waits in the tests.
	testSlotWait = 100 * time.Millisecond
	// takeoverReconnectWait replaces the reconnect wait in the takeover
	// test. It is longer than the test waits, so a replica whose connection
	// ended cannot take its slot back before the spare replica takes it.
	takeoverReconnectWait = 2 * socketTimeout
)

// The Socket Mode payloads of a slash command and a button click.
const (
	commandPayload = `{"command": "/sidekick", "text": "status", "channel_id": "C01ALERTS", "user_id": "U01PERSON", "is_enterprise_install": "false"}`
	clickPayload   = `{"type": "block_actions", "actions": [{"type": "button", "block_id": "sidekick_actions", "action_id": "sidekick_ack", "value": "inv-1"}]}`
)

func TestSocketAcknowledgesAfterStoring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slack func(s *fakehttp.Server)
		check func(t *testing.T, h *harness, f *fakeSocket)
	}{
		{
			name:  "an event is acknowledged only after its insert commits",
			slack: channelInfo,
			check: func(t *testing.T, h *harness, f *fakeSocket) {
				t.Helper()

				var mu sync.Mutex

				storedAtAck := -1
				f.onAck = func(string) {
					mu.Lock()
					defer mu.Unlock()

					storedAtAck = len(h.events(t))
				}

				f.send("env-1", 0, readFixture(t, "requests/signoz-firing.json"))
				waitFor(t, func() bool { return f.acked("env-1") })

				mu.Lock()
				defer mu.Unlock()

				require.Equal(t, 1, storedAtAck, "the event is committed when the acknowledgement arrives")
				require.Equal(t, 1, h.routeJobs(t))
			},
		},
		{
			name:  "a payload sent again is acknowledged and stored once",
			slack: channelInfo,
			check: func(t *testing.T, h *harness, f *fakeSocket) {
				t.Helper()

				f.send("env-1", 0, readFixture(t, "requests/signoz-firing.json"))
				waitFor(t, func() bool { return f.acked("env-1") })

				f.send("env-2", 1, readFixture(t, "requests/signoz-firing.json"))
				waitFor(t, func() bool { return f.acked("env-2") })

				onlyEvent(t, h)
			},
		},
		{
			name: "an event that cannot be stored is not acknowledged",
			slack: func(s *fakehttp.Server) {
				s.Handle(http.MethodGet, "/"+methodReplies, fakehttp.Response{BodyFile: "fixture/slack/replies-not-in-channel.json"})
			},
			check: func(t *testing.T, h *harness, f *fakeSocket) {
				t.Helper()

				f.send("env-bad", 0, readFixture(t, "requests/mention-thread.json"))
				f.send("env-good", 0, readFixture(t, "requests/mention-top.json"))
				waitFor(t, func() bool { return f.acked("env-good") })

				require.Never(t, func() bool { return f.acked("env-bad") }, socketQuiet, socketPoll)
				require.Equal(t, "slack-mention", onlyEvent(t, h).Intake)
			},
		},
		{
			name: "a payload that can never be read is acknowledged",
			check: func(t *testing.T, h *harness, f *fakeSocket) {
				t.Helper()

				f.send("env-1", 0, []byte(`{"type": "event_callback", "event": {"type": "message"}}`))
				waitFor(t, func() bool { return f.acked("env-1") })
				requireNoEvents(t, h)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			if tt.slack != nil {
				tt.slack(h.slack)
			}

			f := newFakeSocket(t, h.slack)
			startSocket(t, h, h.pool, &holders{})
			waitFor(t, func() bool { open, _, _ := f.counts(); return open == 1 })

			tt.check(t, h, f)
		})
	}
}

func TestSocketAnswersCommandsAndInteractions(t *testing.T) {
	t.Parallel()

	const answer = `{"text": "Sidekick is running"}`

	tests := []struct {
		name    string
		kind    string
		payload string
		// handled is whether the Socket gets the fake handlers.
		handled bool
		resp    []byte
		err     error
		// wantAck is the payload of the ack; empty means an empty ack.
		wantAck   string
		wantCalls int
	}{
		{
			name: "a slash command is acknowledged with its handler's response",
			kind: socketmode.RequestTypeSlashCommands, payload: commandPayload, handled: true,
			resp: []byte(answer), wantAck: answer, wantCalls: 1,
		},
		{
			name: "a slash command whose handler gives no response gets an empty ack",
			kind: socketmode.RequestTypeSlashCommands, payload: commandPayload, handled: true, wantCalls: 1,
		},
		{
			name: "a slash command whose handler fails gets an empty ack",
			kind: socketmode.RequestTypeSlashCommands, payload: commandPayload, handled: true,
			resp: []byte(answer), err: errors.New("the command failed"), wantCalls: 1,
		},
		{
			name: "a slash command whose handler answers with text that is not JSON gets an empty ack",
			kind: socketmode.RequestTypeSlashCommands, payload: commandPayload, handled: true,
			resp: []byte("not JSON"), wantCalls: 1,
		},
		{
			name: "a slash command without a handler gets an empty ack",
			kind: socketmode.RequestTypeSlashCommands, payload: commandPayload,
		},
		{
			name: "a button click is acknowledged with its handler's response",
			kind: socketmode.RequestTypeInteractive, payload: clickPayload, handled: true,
			resp: []byte(answer), wantAck: answer, wantCalls: 1,
		},
		{
			name: "a button click whose handler fails gets an empty ack",
			kind: socketmode.RequestTypeInteractive, payload: clickPayload, handled: true,
			err: errors.New("the click failed"), wantCalls: 1,
		},
		{
			name: "a button click without a handler gets an empty ack",
			kind: socketmode.RequestTypeInteractive, payload: clickPayload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			f := newFakeSocket(t, h.slack)
			answers := &fakeAnswers{resp: tt.resp, err: tt.err}

			startSocket(t, h, h.pool, &holders{}, func(o *SocketOptions) {
				if tt.handled {
					o.Commands, o.Interactions = answers, answers
				}
			})
			waitFor(t, func() bool { open, _, _ := f.counts(); return open == 1 })

			f.sendRequest(tt.kind, "env-1", 0, []byte(tt.payload))
			waitFor(t, func() bool { return f.acked("env-1") })

			if tt.wantAck == "" {
				require.Nil(t, f.ackPayload("env-1"))
			} else {
				require.JSONEq(t, tt.wantAck, string(f.ackPayload("env-1")))
			}

			commands, clicks := answers.calls()

			if tt.kind == socketmode.RequestTypeSlashCommands {
				require.Len(t, commands, tt.wantCalls)
				require.Empty(t, clicks)

				if tt.wantCalls > 0 {
					require.Equal(t, "/sidekick", commands[0].Command)
					require.Equal(t, "status", commands[0].Text)
				}
			} else {
				require.Len(t, clicks, tt.wantCalls)
				require.Empty(t, commands)

				if tt.wantCalls > 0 {
					require.Equal(t, slack.InteractionTypeBlockActions, clicks[0].Type)
					require.Len(t, clicks[0].ActionCallback.BlockActions, 1)
					require.Equal(t, "sidekick_ack", clicks[0].ActionCallback.BlockActions[0].ActionID)
				}
			}

			_, _, opened := f.counts()
			require.Equal(t, 1, opened, "the connection carries on")
		})
	}
}

func TestSocketSlots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		replicas int
	}{
		{name: "12 replicas: at most 10 hold a connection", replicas: SocketSlots + 2},
		{name: "fewer replicas than slots: each holds one", replicas: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			f := newFakeSocket(t, h.slack)
			hs := &holders{}

			for range tt.replicas {
				startSocket(t, h, replicaPool(t, h), hs)
			}

			want := min(tt.replicas, SocketSlots)
			waitFor(t, func() bool { now, _ := hs.counts(); open, _, _ := f.counts(); return now == want && open == want })
			require.Never(t, func() bool { _, most := hs.counts(); return most > want }, socketQuiet, socketPoll)
		})
	}
}

func TestSocketTakesOverAFreedSlot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// free frees the slot of r: it stops the replica, or ends its
		// database session.
		free func(t *testing.T, h *harness, r *replica)
		// maxHolders is the most replicas that may hold a connection at
		// once.
		maxHolders int
	}{
		{
			name:       "a replica stops",
			maxHolders: SocketSlots,
			free: func(t *testing.T, _ *harness, r *replica) {
				t.Helper()
				r.stop()
			},
		},
		{
			// The lock is freed as the session ends, but the replica only
			// closes its connection at its next slot check, so another
			// replica may connect first.
			name:       "a replica loses its database session, and so its lock",
			maxHolders: SocketSlots + 1,
			free: func(t *testing.T, h *harness, r *replica) {
				t.Helper()

				// Every test database is on the same server, so the lock is
				// looked up in this test's database only.
				_, err := h.pool.Exec(t.Context(), `
					SELECT pg_terminate_backend(pid) FROM pg_locks
					WHERE locktype = 'advisory' AND classid = $1 AND objid = $2
						AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, slotLockClass, r.slot())
				require.NoError(t, err)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			f := newFakeSocket(t, h.slack)
			hs := &holders{}

			replicas := make([]*replica, 0, SocketSlots+1)
			for range SocketSlots + 1 {
				replicas = append(replicas, startSocketWith(t, h, replicaPool(t, h), hs, takeoverReconnectWait))
			}

			waitFor(t, func() bool { now, _ := hs.counts(); return now == SocketSlots })

			var holder *replica

			for _, r := range replicas {
				if r.isConnected() {
					holder = r

					break
				}
			}

			require.NotNil(t, holder)
			tt.free(t, h, holder)

			waitFor(t, func() bool {
				now, _ := hs.counts()
				_, _, opened := f.counts()

				return now == SocketSlots && opened > SocketSlots && !holder.isConnected()
			})

			_, most := hs.counts()
			require.LessOrEqual(t, most, tt.maxHolders)
		})
	}
}

func TestSocketWithABadAppToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		// wantOpens is whether apps.connections.open is called.
		wantOpens bool
	}{
		{name: "a token Slack refuses is tried again", key: "signingSecret", wantOpens: true},
		{name: "an empty token is read again", key: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			f := newFakeSocket(t, h.slack)
			h.cfg.Slack.AppTokenSecret.Key = tt.key

			startSocket(t, h, h.pool, &holders{})

			if tt.wantOpens {
				waitFor(t, func() bool { return opens(h.slack) >= 2 })
			}

			require.Never(t, func() bool { _, _, opened := f.counts(); return opened > 0 }, socketQuiet, socketPoll)
			require.Equal(t, tt.wantOpens, opens(h.slack) > 0)
		})
	}
}

// fakeAnswers is a CommandHandler and InteractionHandler that answers
// with resp and err, and records what it is given.
type fakeAnswers struct {
	resp []byte
	err  error

	mu       sync.Mutex
	commands []slack.SlashCommand
	clicks   []slack.InteractionCallback
}

// HandleCommand records cmd and answers it.
func (a *fakeAnswers) HandleCommand(_ context.Context, cmd slack.SlashCommand) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.commands = append(a.commands, cmd)

	return a.resp, a.err
}

// HandleInteraction records cb and answers it.
func (a *fakeAnswers) HandleInteraction(_ context.Context, cb slack.InteractionCallback) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.clicks = append(a.clicks, cb)

	return a.resp, a.err
}

// calls returns the commands and interactions handled so far.
func (a *fakeAnswers) calls() ([]slack.SlashCommand, []slack.InteractionCallback) {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Clone(a.commands), slices.Clone(a.clicks)
}

// replica is one running Socket, as one replica of Sidekick runs it.
type replica struct {
	stop func()

	mu        sync.Mutex
	connected bool
	held      int32
}

// holders counts the replicas that hold a connection, now and at most.
type holders struct {
	mu   sync.Mutex
	now  int
	most int
}

// add changes the count by n.
func (c *holders) add(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now += n
	c.most = max(c.most, c.now)
}

// counts returns how many replicas hold a connection now, and the most that
// held one at once.
func (c *holders) counts() (now, most int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now, c.most
}

// isConnected reports whether the replica holds a connection.
func (r *replica) isConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.connected
}

// slot returns the slot of the replica's last connection.
func (r *replica) slot() int32 {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.held
}

// startSocket runs a Socket for h's handler with pool until the test ends,
// with short waits, counting it in hs while it holds a connection. Each of
// with changes the options first. Run must return nil once stopped.
func startSocket(t *testing.T, h *harness, pool *pgxpool.Pool, hs *holders, with ...func(*SocketOptions)) *replica {
	t.Helper()

	return startSocketWith(t, h, pool, hs, testSlotWait, with...)
}

// startSocketWith is startSocket with reconnect as the wait after a
// connection ends.
func startSocketWith(t *testing.T, h *harness, pool *pgxpool.Pool, hs *holders, reconnect time.Duration, with ...func(*SocketOptions)) *replica {
	t.Helper()

	opts := SocketOptions{
		Handler:  h.handler,
		Pool:     pool,
		Secrets:  secrets.NewDir(h.cfg.Secrets.Dir),
		AppToken: h.cfg.Slack.AppTokenSecret,
		APIURL:   h.cfg.Slack.APIURL,
		HTTP:     h.slack.Client(),
		Logger:   slog.New(slog.DiscardHandler),
	}

	for _, change := range with {
		change(&opts)
	}

	s := NewSocket(opts)
	s.slotRetry, s.slotCheck, s.reconnectWait = testSlotWait, testSlotWait, reconnect
	s.clientOptions = []socketmode.Option{socketmode.OptionPingInterval(socketTimeout)}

	r := &replica{}
	s.connected = func(slot int32) {
		r.mu.Lock()
		defer r.mu.Unlock()

		if !r.connected {
			hs.add(1)
		}

		r.connected, r.held = true, slot
	}
	s.ended = func(int32) {
		r.mu.Lock()
		defer r.mu.Unlock()

		if r.connected {
			hs.add(-1)
		}

		r.connected = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- s.Run(ctx) }()

	var once sync.Once

	r.stop = func() {
		once.Do(func() {
			cancel()
			require.NoError(t, <-done)
		})
	}

	t.Cleanup(r.stop)

	return r
}

// replicaPool returns a pool of its own on h's database, as another
// replica has.
func replicaPool(t *testing.T, h *harness) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.NewWithConfig(t.Context(), h.pool.Config().Copy())
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

// waitFor waits until cond holds, or fails the test after socketTimeout.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	require.Eventually(t, cond, socketTimeout, socketPoll)
}

// opens counts the calls of apps.connections.open.
func opens(s *fakehttp.Server) int {
	n := 0

	for _, r := range s.Requests() {
		if r.Path == "/apps.connections.open" {
			n++
		}
	}

	return n
}

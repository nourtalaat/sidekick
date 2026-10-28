package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// MaxStreams is how many event streams one replica serves at once.
	MaxStreams = 100
	// hubListenRetry is the wait after the listen connection failed,
	// before it listens again. Streams read the database every heartbeat
	// meanwhile.
	hubListenRetry = 5 * time.Second
	// hubCloseTimeout bounds closing the listen connection.
	hubCloseTimeout = 5 * time.Second
)

// What a notification says changed, and so what a woken stream looks at.
const (
	changeSteps uint32 = 1 << iota
	changeStatus
	changeWatchers

	changeAll = changeSteps | changeStatus | changeWatchers
)

// The kinds of change in the payload of an investigation_updates
// notification, after the investigation's ID.
const (
	payloadStep     = "step"
	payloadStatus   = "status"
	payloadWatchers = "watchers"
)

// errTooManyStreams means the replica serves MaxStreams streams already.
var errTooManyStreams = errors.New("too many open streams")

// Hub fans the investigation_updates notifications of the database out to
// the event streams that this replica serves. It holds one connection that
// listens, so a replica that does not run an investigation still hears
// about it. Make it with NewHub, run it as a task, and pass it in
// Options.Hub. Its streams end when Run does.
//
// A notification carries no data. It sets what changed on each
// subscription of the investigation and wakes it through a channel of one
// place, without blocking: a slow stream only gets its changes joined into
// one wake-up, and the hub never keeps more than a flag set per stream.
// The stream then reads the rows it needs from the database.
type Hub struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	retry time.Duration
	max   int

	mu    sync.Mutex
	subs  map[uuid.UUID]map[*Subscription]struct{}
	count int

	stopped chan struct{}
	stop    sync.Once
}

// Subscription is one stream's interest in an investigation. Get it with
// Hub.Subscribe, wait on Wake, and call Close when the stream ends.
type Subscription struct {
	hub     *Hub
	id      uuid.UUID
	wake    chan struct{}
	pending atomic.Uint32
	closed  sync.Once
}

// HubOptions set up a Hub. Pool is required.
type HubOptions struct {
	// Pool is the database to listen on, with a connection of the pool.
	Pool *pgxpool.Pool
	// MaxStreams is how many streams the hub serves at once. Zero means
	// MaxStreams.
	MaxStreams int
	// ListenRetry is the wait before the hub listens again after its
	// connection failed. Zero means five seconds.
	ListenRetry time.Duration
	// Logger gets a line when the connection fails. Nil logs nothing.
	Logger *slog.Logger
}

// NewHub returns a Hub.
func NewHub(opts HubOptions) *Hub {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Hub{
		pool:    opts.Pool,
		log:     log,
		retry:   cmp.Or(opts.ListenRetry, hubListenRetry),
		max:     cmp.Or(opts.MaxStreams, MaxStreams),
		subs:    map[uuid.UUID]map[*Subscription]struct{}{},
		stopped: make(chan struct{}),
	}
}

// Run listens for notifications until ctx ends, and returns nil then. When
// its connection fails it listens again after a pause. When it returns,
// every subscription is told to end.
func (h *Hub) Run(ctx context.Context) error {
	defer h.stop.Do(func() { close(h.stopped) })

	for ctx.Err() == nil {
		if err := h.listen(ctx); err != nil && ctx.Err() == nil {
			h.log.WarnContext(ctx, "listening for investigation updates failed; streams read the database every heartbeat until it works again", "error", err.Error())
		}

		select {
		case <-ctx.Done():
		case <-time.After(h.retry):
		}
	}

	return nil
}

// Subscribe starts a subscription to the investigation with id. It fails
// with errTooManyStreams when the replica serves MaxStreams streams.
func (h *Hub) Subscribe(id uuid.UUID) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.count >= h.max {
		return nil, errTooManyStreams
	}

	sub := &Subscription{hub: h, id: id, wake: make(chan struct{}, 1)}

	if h.subs[id] == nil {
		h.subs[id] = map[*Subscription]struct{}{}
	}

	h.subs[id][sub] = struct{}{}
	h.count++

	return sub, nil
}

// Wake returns the channel that gets a value when something changed.
func (s *Subscription) Wake() <-chan struct{} {
	return s.wake
}

// Take returns what changed since the last Take, as the change flags.
func (s *Subscription) Take() uint32 {
	return s.pending.Swap(0)
}

// Stopped returns a channel that closes when the hub stops, which ends the
// stream.
func (s *Subscription) Stopped() <-chan struct{} {
	return s.hub.stopped
}

// Close ends the subscription. Calling it again does nothing.
func (s *Subscription) Close() {
	s.closed.Do(func() {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()

		delete(s.hub.subs[s.id], s)

		if len(s.hub.subs[s.id]) == 0 {
			delete(s.hub.subs, s.id)
		}

		s.hub.count--
	})
}

// signal records that changes happened and wakes the stream, if it is not
// awake already.
func (s *Subscription) signal(changes uint32) {
	s.pending.Or(changes)

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// listen holds one connection that listens for notifications, until it
// fails. Once it listens, every subscription is woken, since a change may
// have been missed while nothing listened.
func (h *Hub) listen(ctx context.Context) error {
	pooled, err := h.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection: %w", err)
	}

	// The connection leaves the pool, so it never goes back still
	// listening.
	conn := pooled.Hijack()

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hubCloseTimeout)
		defer cancel()

		if err := conn.Close(closeCtx); err != nil {
			h.log.WarnContext(ctx, "closing the investigation updates connection failed", "error", err.Error())
		}
	}()

	if err := db.New(conn).ListenInvestigationUpdates(ctx); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	h.wakeAll()

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for a notification: %w", err)
		}

		h.notify(n.Payload)
	}
}

// notify wakes the subscriptions of the investigation a notification names.
func (h *Hub) notify(payload string) {
	idText, rest, _ := strings.Cut(payload, ":")

	id, err := uuid.Parse(idText)
	if err != nil {
		return
	}

	kind, _, _ := strings.Cut(rest, ":")

	changes := changeAll

	switch kind {
	case payloadStep:
		changes = changeSteps
	case payloadStatus:
		changes = changeStatus
	case payloadWatchers:
		changes = changeWatchers
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	for sub := range h.subs[id] {
		sub.signal(changes)
	}
}

// wakeAll wakes every subscription, for everything.
func (h *Hub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, subs := range h.subs {
		for sub := range subs {
			sub.signal(changeAll)
		}
	}
}

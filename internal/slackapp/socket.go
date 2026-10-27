package slackapp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SocketSlots is how many replicas hold a Socket Mode connection at once.
// Slack allows 10 connections per app and sends each payload to one of
// them.
const SocketSlots = 10

// slotLockClass is the first key of the slot advisory locks; the slot
// number is the second. Two-key advisory locks never collide with the
// one-key lock that migrations take.
const slotLockClass int32 = 0x534b0001

const (
	// slotRetry is how long a replica without a slot waits before it tries
	// again, so it takes over when a replica that held one stops.
	slotRetry = 30 * time.Second
	// slotCheck is how often the session that holds the slot is checked. A
	// lost session has lost its lock too, so the connection is closed.
	slotCheck = 30 * time.Second
	// reconnectWait is the wait after a connection that ended with an
	// error, such as a revoked app token, before the next try.
	reconnectWait = 30 * time.Second
	// maxInFlight is the most payloads one connection handles at once.
	maxInFlight = 32
	// slotCloseTimeout bounds closing a slot's database session.
	slotCloseTimeout = 5 * time.Second
)

// authErrors are the Slack error codes that mean the app token is wrong.
var authErrors = []string{"invalid_auth", "not_authed", "account_inactive", "token_revoked"}

// errEmptyAppToken is returned when the app token's file is empty.
var errEmptyAppToken = errors.New("the app token is empty")

// SocketOptions set up a Socket. Handler, Pool, Secrets, AppToken and
// Logger are required.
type SocketOptions struct {
	// Handler stores the payloads that arrive.
	Handler *Handler
	// Pool holds the slot locks, on a connection of their own.
	Pool *pgxpool.Pool
	// Secrets resolves the app token each time a connection opens, so a
	// rotated token is used from the next connection on.
	Secrets registry.Secrets
	// AppToken is slack.appTokenSecret.
	AppToken registry.SecretRef
	// APIURL is the Slack Web API base URL, ending in "/". Empty means
	// DefaultAPIURL.
	APIURL string
	// HTTP calls apps.connections.open. Nil means httpclient.New().
	HTTP *http.Client
	// Logger gets the connection's life cycle. Payloads are never logged.
	Logger *slog.Logger
}

// Socket receives events over one Socket Mode connection while this
// replica holds one of SocketSlots slots, and hands every Events API
// payload to the Handler. It acknowledges a payload only once the Handler
// has stored it.
type Socket struct {
	handler  *Handler
	pool     *pgxpool.Pool
	secrets  registry.Secrets
	appToken registry.SecretRef
	apiURL   string
	http     *http.Client
	log      *slog.Logger

	// The waits and options below are constants outside tests.
	slotRetry     time.Duration
	slotCheck     time.Duration
	reconnectWait time.Duration
	clientOptions []socketmode.Option
	// connected, when set, is called with the slot each time a connection
	// opens, and ended when the connection on a slot has ended, before
	// the slot is freed.
	connected func(slot int32)
	ended     func(slot int32)
}

// NewSocket returns a Socket. Call Run to connect.
func NewSocket(opts SocketOptions) *Socket {
	return &Socket{
		handler:       opts.Handler,
		pool:          opts.Pool,
		secrets:       opts.Secrets,
		appToken:      opts.AppToken,
		apiURL:        cmp.Or(opts.APIURL, DefaultAPIURL),
		http:          cmp.Or(opts.HTTP, httpclient.New()),
		log:           opts.Logger,
		slotRetry:     slotRetry,
		slotCheck:     slotCheck,
		reconnectWait: reconnectWait,
	}
}

// Run takes a slot, connects and receives events until ctx ends. When the
// connection or the slot is lost, it takes a slot and connects again. It
// returns nil once ctx ends.
func (s *Socket) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		conn, slot, ok := s.takeSlot(ctx)
		if !ok {
			break
		}

		s.serveSlot(ctx, conn, slot)
	}

	return nil
}

// serveSlot connects while it holds the slot on conn, then frees the slot.
// When the connection ended before ctx did, it logs why and waits
// reconnectWait.
func (s *Socket) serveSlot(ctx context.Context, conn *pgx.Conn, slot int32) {
	err := s.connect(ctx, conn, slot)

	if s.ended != nil {
		s.ended(slot)
	}

	s.release(conn)

	if ctx.Err() != nil {
		return
	}

	if refusedToken(err) {
		s.log.ErrorContext(ctx, "Slack refused the app token; check slack.appTokenSecret", "slot", slot, "wait", s.reconnectWait.String())
	} else {
		s.log.WarnContext(ctx, "Slack Socket Mode connection ended; connecting again", "slot", slot, "error", errText(err), "wait", s.reconnectWait.String())
	}

	wait(ctx, s.reconnectWait)
}

// takeSlot waits until this replica holds a slot, and returns the
// database session that holds it. ok is false when ctx ended first.
func (s *Socket) takeSlot(ctx context.Context) (conn *pgx.Conn, slot int32, ok bool) {
	for {
		conn, slot, err := s.tryTakeSlot(ctx)

		switch {
		case err != nil && ctx.Err() == nil:
			s.log.WarnContext(ctx, "could not take a Slack Socket Mode slot", "error", err.Error())
		case conn != nil:
			return conn, slot, true
		default:
			s.log.DebugContext(ctx, "every Slack Socket Mode slot is taken; not connecting for now", "slots", SocketSlots)
		}

		if !wait(ctx, s.slotRetry) {
			return nil, 0, false
		}
	}
}

// tryTakeSlot takes a database session of its own and tries each slot on
// it. It returns the session when one was free, or nil.
func (s *Socket) tryTakeSlot(ctx context.Context) (*pgx.Conn, int32, error) {
	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("acquire a database connection: %w", err)
	}

	// The session must outlive the pool's use of it, since the lock lasts
	// as long as the session.
	conn := pooled.Hijack()
	queries := db.New(conn)

	for slot := range int32(SocketSlots) {
		taken, err := queries.TryTakeSlackSlot(ctx, db.TryTakeSlackSlotParams{LockClass: slotLockClass, Slot: slot})
		if err != nil {
			s.release(conn)

			return nil, 0, fmt.Errorf("try slot %d: %w", slot, err)
		}

		if taken {
			return conn, slot, nil
		}
	}

	s.release(conn)

	return nil, 0, nil
}

// connect opens a Socket Mode connection and handles its events until ctx
// ends, the connection fails, or the session that holds the slot is lost.
func (s *Socket) connect(ctx context.Context, conn *pgx.Conn, slot int32) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	token, err := secrets.ResolveText(ctx, s.secrets, s.appToken)
	if err != nil {
		return fmt.Errorf("read the app token: %w", err)
	}

	if token == "" {
		return errEmptyAppToken
	}

	api := slack.New("", slack.OptionAppLevelToken(token), slack.OptionAPIURL(s.apiURL), slack.OptionHTTPClient(s.http))
	client := socketmode.New(api, s.clientOptions...)

	lost := make(chan error, 1)

	go func() {
		lost <- s.watchSlot(ctx, conn)

		cancel()
	}()

	done := make(chan error, 1)

	go func() { done <- client.RunContext(ctx) }()

	var inFlight sync.WaitGroup

	defer inFlight.Wait()

	sem := make(chan struct{}, maxInFlight)

	for {
		select {
		case err := <-done:
			// The context ends at shutdown, or when the slot is lost.
			stopped := ctx.Err() != nil
			cancel()

			if stopped {
				return fmt.Errorf("connection stopped: %w", <-lost)
			}

			return fmt.Errorf("run the connection: %w", err)
		case evt := <-client.Events:
			s.dispatch(ctx, client, evt, slot, sem, &inFlight)
		}
	}
}

// watchSlot checks the slot's session every slotCheck until ctx ends, and
// returns the error that shows the session, and so the lock, is gone.
func (s *Socket) watchSlot(ctx context.Context, conn *pgx.Conn) error {
	ticker := time.NewTicker(s.slotCheck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stop watching the slot: %w", context.Cause(ctx))
		case <-ticker.C:
			if err := conn.Ping(ctx); err != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("stop watching the slot: %w", context.Cause(ctx))
				}

				return fmt.Errorf("check the slot's database session: %w", err)
			}
		}
	}
}

// dispatch handles one event of the connection. An Events API payload is
// handled in its own goroutine, at most maxInFlight at once, and
// acknowledged once it is stored; one that cannot be stored is not
// acknowledged, so Slack sends it again. Commands and clicks are not
// handled yet and are acknowledged at once, so Slack does not send them
// again.
func (s *Socket) dispatch(ctx context.Context, client *socketmode.Client, evt socketmode.Event, slot int32, sem chan struct{}, inFlight *sync.WaitGroup) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		req := evt.Request
		if req == nil {
			return
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}

		inFlight.Go(func() {
			defer func() { <-sem }()

			s.handle(ctx, client, *req)
		})
	case socketmode.EventTypeSlashCommand, socketmode.EventTypeInteractive:
		if evt.Request != nil {
			s.ack(ctx, client, evt.Request.EnvelopeID)
		}
	case socketmode.EventTypeConnected:
		s.log.InfoContext(ctx, "Slack Socket Mode connected", "slot", slot)

		if s.connected != nil {
			s.connected(slot)
		}
	case socketmode.EventTypeInvalidAuth:
		s.log.ErrorContext(ctx, "Slack refused the app token; check slack.appTokenSecret", "slot", slot)
	case socketmode.EventTypeConnectionError:
		s.log.WarnContext(ctx, "could not connect to Slack Socket Mode; trying again", "slot", slot, "error", connectionError(evt.Data))
	default:
		s.log.DebugContext(ctx, "Slack Socket Mode event", "type", string(evt.Type))
	}
}

// handle stores one payload and acknowledges it once stored. A payload that
// can never be read is acknowledged too, so Slack stops sending it.
func (s *Socket) handle(ctx context.Context, client *socketmode.Client, req socketmode.Request) {
	err := s.handler.Handle(ctx, req.Payload)

	switch {
	case errors.Is(err, ErrInvalidPayload):
		s.log.WarnContext(ctx, "Slack payload is not valid; acknowledging it so Slack stops sending it", "error", err.Error())
	case err != nil:
		s.log.ErrorContext(ctx, "Slack event not stored; Slack sends it again", "error", err.Error(), "retry_attempt", req.RetryAttempt)

		return
	}

	s.ack(ctx, client, req.EnvelopeID)
}

// ack acknowledges the payload with envelopeID.
func (s *Socket) ack(ctx context.Context, client *socketmode.Client, envelopeID string) {
	if err := client.AckCtx(ctx, envelopeID, nil); err != nil {
		s.log.WarnContext(ctx, "could not acknowledge a Slack payload; Slack sends it again", "error", err.Error())
	}
}

// release closes the slot's session, which frees its lock.
func (s *Socket) release(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), slotCloseTimeout)
	defer cancel()

	if err := conn.Close(ctx); err != nil {
		s.log.DebugContext(ctx, "close the Slack slot's database session", "error", err.Error())
	}
}

// refusedToken reports whether err is Slack refusing the app token.
func refusedToken(err error) bool {
	resp, ok := errors.AsType[slack.SlackErrorResponse](err)

	return ok && slices.Contains(authErrors, resp.Err)
}

// connectionError returns the error text of a connection_error event.
func connectionError(data any) string {
	if e, ok := data.(*slack.ConnectionErrorEvent); ok && e.ErrorObj != nil {
		return e.ErrorObj.Error()
	}

	return "unknown"
}

// errText returns err's text, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// wait sleeps for d and reports true, or reports false when ctx ends
// first.
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

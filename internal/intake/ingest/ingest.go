// Package ingest stores the events that intake receives, whether from a
// webhook endpoint or from Slack. It redacts each event, keeps its key
// stable and short, takes trust from the caller's config rather than the
// payload, and writes every event of a delivery with its route_event job in
// one transaction.
//
// The route_event job's payload is {"event_id": "<the event's ID>"}.
package ingest

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// StatusReceived is the events.status that intake writes.
const StatusReceived = "received"

// maxKeyBytes is the longest key stored as it is. Longer keys are stored
// as their hash, so the unique index on (intake, key) stays small.
const maxKeyBytes = 256

// hashedKeyPrefix starts a key that is stored as its hash.
const hashedKeyPrefix = "sha256:"

// routeJobKeyPrefix starts the unique key of an event's route_event job.
const routeJobKeyPrefix = "route_event:"

// routeEventPayload is the payload of a route_event job.
type routeEventPayload struct {
	EventID uuid.UUID `json:"event_id"`
}

// storedLink is one entry of events.links.
type storedLink struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Writer stores events. It is safe for concurrent use.
type Writer struct {
	pool     *pgxpool.Pool
	redactor *redact.Redactor
}

// Delivery is what one request or Slack payload brought in.
type Delivery struct {
	// Intake is the webhook endpoint or Slack listener name.
	Intake string
	// Trusted comes from the intake's config, never from the payload. It
	// applies to every event of the delivery.
	Trusted bool
	// ReceivedAt is when the delivery arrived.
	ReceivedAt time.Time
	// Events are the events the delivery holds. Each needs a Key.
	Events []event.Event
}

// Stored is what happened to one event of a delivery.
type Stored struct {
	// ID is the new event's ID; it is the zero UUID for a duplicate.
	ID uuid.UUID
	// Key is the key as it is stored: redacted keys and long keys are
	// hashed.
	Key string
	// Status is StatusReceived; empty for a duplicate.
	Status string
	// Duplicate is true when the intake already had an event with the key.
	Duplicate bool
}

// NewWriter returns a Writer that redacts events with redactor and stores
// them in pool.
func NewWriter(pool *pgxpool.Pool, redactor *redact.Redactor) *Writer {
	return &Writer{pool: pool, redactor: redactor}
}

// Store writes the events of d in one transaction and returns what
// happened to each, in order. Each event that is new gets a route_event
// job in the same transaction; a resolved event too, so the router can
// link it to its group. An event whose key the intake already has is left
// as it is and reported as a duplicate. When Store returns without an
// error, every event is committed.
func (w *Writer) Store(ctx context.Context, d Delivery) (stored []Stored, err error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	queries := db.New(tx)
	stored = make([]Stored, 0, len(d.Events))

	for i, ev := range d.Events {
		params, err := w.row(d, ev)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}

		id, err := queries.InsertEvent(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			stored = append(stored, Stored{Key: params.Key, Duplicate: true})

			continue
		}

		if err != nil {
			return nil, fmt.Errorf("insert event %d: %w", i, err)
		}

		if err := enqueueRoute(ctx, tx, id); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}

		stored = append(stored, Stored{ID: id, Key: params.Key, Status: params.Status})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return stored, nil
}

// row builds the events row for ev. Every text from the payload is
// redacted, and a link whose URL redaction would change is dropped. The key
// is kept as it is unless the redactor finds something in it, it is too
// long, or it is not valid text; then it is stored as its SHA-256, which
// still makes a repeated delivery map to the same row.
func (w *Writer) row(d Delivery, ev event.Event) (db.InsertEventParams, error) {
	labels, err := w.redactedLabels(ev.Labels)
	if err != nil {
		return db.InsertEventParams{}, fmt.Errorf("redact labels: %w", err)
	}

	links, err := w.redactedLinks(ev.Links)
	if err != nil {
		return db.InsertEventParams{}, fmt.Errorf("redact links: %w", err)
	}

	fingerprint := ev.Fingerprint
	if fingerprint == "" {
		fingerprint = event.Fingerprint(d.Intake, ev.AlertName, ev.Labels, slices.Collect(maps.Keys(ev.Labels)))
	}

	p := db.InsertEventParams{
		ID:          ids.New(),
		Intake:      d.Intake,
		Key:         w.storedKey(ev.Key),
		Kind:        string(ev.Kind),
		SenderKind:  optional(string(ev.Sender.Kind)),
		SenderID:    w.redactedText(ev.Sender.ID),
		SenderName:  w.redactedText(ev.Sender.Name),
		Trusted:     d.Trusted,
		AlertName:   w.redactedText(ev.AlertName),
		Title:       w.redactedText(ev.Title),
		Body:        w.redactedText(ev.Text),
		Service:     w.redactedText(ev.Service),
		Severity:    optional(string(cmp.Or(ev.Severity, event.SeverityUnknown))),
		Labels:      labels,
		Links:       links,
		Fingerprint: store.Text(w.redactor.Text(fingerprint).Text),
		Raw:         w.redactedJSON(ev.Raw),
		Status:      StatusReceived,
		StartsAt:    optionalTime(ev.StartsAt),
		EndsAt:      optionalTime(ev.EndsAt),
		ReceivedAt:  d.ReceivedAt,
	}

	if ev.Slack != nil {
		slack, err := json.Marshal(ev.Slack)
		if err != nil {
			return db.InsertEventParams{}, fmt.Errorf("encode the Slack reference: %w", err)
		}

		p.Slack = w.redactedJSON(slack)
	}

	if ev.Teams != nil {
		teams, err := json.Marshal(ev.Teams)
		if err != nil {
			return db.InsertEventParams{}, fmt.Errorf("encode the Teams reference: %w", err)
		}

		p.Teams = w.redactedJSON(teams)
	}

	return p, nil
}

// storedKey returns key as it is stored.
func (w *Writer) storedKey(key string) string {
	if len(key) <= maxKeyBytes && store.ValidText(key) && w.redactor.Text(key).Counts == nil {
		return key
	}

	sum := sha256.Sum256([]byte(key))

	return hashedKeyPrefix + hex.EncodeToString(sum[:])
}

// redactedText redacts s and makes it fit a text column. Empty text gives
// nil, for NULL.
func (w *Writer) redactedText(s string) *string {
	if s == "" {
		return nil
	}

	return optional(store.Text(w.redactor.Text(s).Text))
}

// redactedLabels redacts the labels, names and values both, as a JSON
// object.
func (w *Writer) redactedLabels(labels map[string]string) (json.RawMessage, error) {
	if labels == nil {
		labels = map[string]string{}
	}

	raw, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("encode labels: %w", err)
	}

	return w.redactedJSON(raw), nil
}

// redactedLinks returns the links as a JSON array of title and URL, with
// each title redacted. A link whose URL the redactor would change, or that
// a text column cannot hold, is dropped, because a URL with a placeholder in
// it is broken. No links give nil, for NULL.
func (w *Writer) redactedLinks(links []event.Link) ([]byte, error) {
	kept := make([]storedLink, 0, len(links))

	for _, l := range links {
		if l.URL == "" || !store.ValidText(l.URL) || w.redactor.Text(l.URL).Text != l.URL {
			continue
		}

		kept = append(kept, storedLink{Title: store.Text(w.redactor.Text(l.Title).Text), URL: l.URL})
	}

	if len(kept) == 0 {
		return nil, nil
	}

	raw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encode links: %w", err)
	}

	return raw, nil
}

// redactedJSON redacts raw and makes it fit a jsonb column. Empty input
// gives nil, for NULL.
func (w *Writer) redactedJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}

	out, _ := w.redactor.JSON(raw)

	return store.JSON(out)
}

// enqueueRoute adds the route_event job of the event with id.
func enqueueRoute(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	payload, err := json.Marshal(routeEventPayload{EventID: id})
	if err != nil {
		return fmt.Errorf("encode the route_event payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, tx, queue.NewJob{Kind: queue.KindRouteEvent, Payload: payload, UniqueKey: routeJobKeyPrefix + id.String()})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue route_event: %w", err)
	}

	return nil
}

// optional turns "" into nil, for NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

// optionalTime turns the zero time into nil, for NULL.
func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}

	return &t
}

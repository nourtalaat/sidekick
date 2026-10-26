package webhook

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

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The values of events.status that intake writes.
const (
	statusReceived = "received"
	statusSkipped  = "skipped"
)

// skipReasonResolved is why resolved events are not routed yet.
const skipReasonResolved = "resolved events are linked in M1"

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

// storedEvent is what happened to one event of a delivery.
type storedEvent struct {
	id        uuid.UUID
	key       string
	status    string
	duplicate bool
}

// store writes the events in one transaction. Each event that is new gets
// a route_event job in the same transaction, unless it is resolved, which
// is stored as skipped. An event whose key the intake already has is left
// as it is and reported as a duplicate.
func (h *Handler) store(ctx context.Context, ep endpoint, req intake.Request, events []event.Event) (stored []storedEvent, err error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	queries := db.New(tx)
	stored = make([]storedEvent, 0, len(events))

	for i, ev := range events {
		params, err := h.row(ep, req, ev)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}

		id, err := queries.InsertEvent(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			stored = append(stored, storedEvent{key: params.Key, duplicate: true})

			continue
		}

		if err != nil {
			return nil, fmt.Errorf("insert event %d: %w", i, err)
		}

		if params.Status == statusReceived {
			if err := enqueueRoute(ctx, tx, id); err != nil {
				return nil, fmt.Errorf("event %d: %w", i, err)
			}
		}

		stored = append(stored, storedEvent{id: id, key: params.Key, status: params.Status})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return stored, nil
}

// row builds the events row for ev. Every text from the payload is
// redacted, and a link whose URL redaction would change is dropped. The key is kept as it is unless the redactor finds something
// in it, it is too long, or it is not valid text; then it is stored as its
// SHA-256, which still makes a repeated delivery map to the same row.
// Trust comes from the endpoint's config.
func (h *Handler) row(ep endpoint, req intake.Request, ev event.Event) (db.InsertEventParams, error) {
	labels, err := h.redactedLabels(ev.Labels)
	if err != nil {
		return db.InsertEventParams{}, fmt.Errorf("redact labels: %w", err)
	}

	links, err := h.redactedLinks(ev.Links)
	if err != nil {
		return db.InsertEventParams{}, fmt.Errorf("redact links: %w", err)
	}

	fingerprint := ev.Fingerprint
	if fingerprint == "" {
		fingerprint = event.Fingerprint(ep.name, ev.AlertName, ev.Labels, slices.Collect(maps.Keys(ev.Labels)))
	}

	p := db.InsertEventParams{
		ID:          ids.New(),
		Intake:      ep.name,
		Key:         h.storedKey(ev.Key),
		Kind:        string(ev.Kind),
		SenderKind:  optional(string(ev.Sender.Kind)),
		SenderID:    h.redactedText(ev.Sender.ID),
		SenderName:  h.redactedText(ev.Sender.Name),
		Trusted:     ep.cfg.Trusted,
		AlertName:   h.redactedText(ev.AlertName),
		Title:       h.redactedText(ev.Title),
		Body:        h.redactedText(ev.Text),
		Service:     h.redactedText(ev.Service),
		Severity:    optional(string(cmp.Or(ev.Severity, event.SeverityUnknown))),
		Labels:      labels,
		Links:       links,
		Fingerprint: dbText(h.redactor.Text(fingerprint).Text),
		Raw:         h.redactedJSON(ev.Raw),
		Status:      statusReceived,
		StartsAt:    optionalTime(ev.StartsAt),
		EndsAt:      optionalTime(ev.EndsAt),
		ReceivedAt:  req.ReceivedAt,
	}

	if ev.Slack != nil {
		slack, err := json.Marshal(ev.Slack)
		if err != nil {
			return db.InsertEventParams{}, fmt.Errorf("encode the Slack reference: %w", err)
		}

		p.Slack = h.redactedJSON(slack)
	}

	if ev.Kind == event.KindResolved {
		p.Status = statusSkipped
		p.SkipReason = optional(skipReasonResolved)
	}

	return p, nil
}

// storedKey returns key as it is stored.
func (h *Handler) storedKey(key string) string {
	if len(key) <= maxKeyBytes && validText(key) && h.redactor.Text(key).Counts == nil {
		return key
	}

	sum := sha256.Sum256([]byte(key))

	return hashedKeyPrefix + hex.EncodeToString(sum[:])
}

// redactedText redacts s and makes it fit a text column. Empty text gives
// nil, for NULL.
func (h *Handler) redactedText(s string) *string {
	if s == "" {
		return nil
	}

	return optional(dbText(h.redactor.Text(s).Text))
}

// redactedLabels redacts the labels, names and values both, as a JSON
// object.
func (h *Handler) redactedLabels(labels map[string]string) (json.RawMessage, error) {
	if labels == nil {
		labels = map[string]string{}
	}

	raw, err := json.Marshal(labels)
	if err != nil {
		return nil, fmt.Errorf("encode labels: %w", err)
	}

	return h.redactedJSON(raw), nil
}

// redactedLinks returns the links as a JSON array of title and URL, with
// each title redacted. A link whose URL the redactor would change, or that
// a text column cannot hold, is dropped, because a URL with a placeholder in
// it is broken. No links give nil, for NULL.
func (h *Handler) redactedLinks(links []event.Link) ([]byte, error) {
	kept := make([]storedLink, 0, len(links))

	for _, l := range links {
		if l.URL == "" || !validText(l.URL) || h.redactor.Text(l.URL).Text != l.URL {
			continue
		}

		kept = append(kept, storedLink{Title: dbText(h.redactor.Text(l.Title).Text), URL: l.URL})
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
func (h *Handler) redactedJSON(raw []byte) []byte {
	if len(raw) == 0 {
		return nil
	}

	out, _ := h.redactor.JSON(raw)

	return dbJSON(out)
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

package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The values of investigation_inbox.kind.
const (
	// InboxNewEvent is an event that joined the investigation's group.
	InboxNewEvent = "new_event"
	// InboxResolved is a recovery event of an alert in the group.
	InboxResolved = "resolved"
)

var errUnknownInboxKind = errors.New("unknown inbox kind")

// inboxEvent is the body of an inbox item about an event.
type inboxEvent struct {
	EventID uuid.UUID `json:"event_id"`
}

// AddEventToInbox adds the event with eventID to the inbox of an
// investigation, as kind InboxNewEvent or InboxResolved. A running
// investigation sees it before the model's next turn; an investigation
// that has ended keeps it. Call it in the transaction that links the event
// to the group.
func AddEventToInbox(ctx context.Context, q db.DBTX, investigationID uuid.UUID, kind string, eventID uuid.UUID) error {
	if kind != InboxNewEvent && kind != InboxResolved {
		return fmt.Errorf("%w %q", errUnknownInboxKind, kind)
	}

	body, err := json.Marshal(inboxEvent{EventID: eventID})
	if err != nil {
		return fmt.Errorf("encode the inbox item: %w", err)
	}

	if err := db.New(q).InsertInboxItem(ctx, db.InsertInboxItemParams{InvestigationID: investigationID, Kind: kind, Body: body}); err != nil {
		return fmt.Errorf("add event %s to the inbox of %s: %w", eventID, investigationID, err)
	}

	return nil
}

// deliverInbox saves every item that waits in the investigation's inbox as
// one inbox step, a user message the model sees at its next turn. The same
// transaction marks the items with the step's seq, so a resumed run never
// delivers them twice. It saves nothing when no item waits.
func (r *run) deliverInbox(ctx context.Context) error {
	queries := db.New(r.h.opts.Pool)

	items, err := queries.ListWaitingInbox(ctx, r.inv.ID)
	if err != nil {
		return fmt.Errorf("list the inbox: %w", err)
	}

	if len(items) == 0 {
		return nil
	}

	text, err := r.inboxText(ctx, items)
	if err != nil {
		return fmt.Errorf("read the inbox: %w", err)
	}

	itemIDs := make([]int64, len(items))
	for i, item := range items {
		itemIDs[i] = item.ID
	}

	s := newStep{kind: kindInbox, parts: []model.Part{model.Text{Text: text}}, status: statusOK, inbox: itemIDs, startedAt: time.Now()}
	if _, err := r.save(ctx, s); err != nil {
		return fmt.Errorf("deliver %d inbox items: %w", len(items), err)
	}

	return nil
}

// inboxText writes the message that delivers items. An item whose event no
// longer exists is left out of the text but still marked delivered.
func (r *run) inboxText(ctx context.Context, items []db.InvestigationInbox) (string, error) {
	eventIDs := make([]uuid.UUID, 0, len(items))

	for _, item := range items {
		if item.Kind != InboxNewEvent && item.Kind != InboxResolved {
			return "", fmt.Errorf("item %d: %w %q", item.ID, errUnknownInboxKind, item.Kind)
		}

		var body inboxEvent
		if err := json.Unmarshal(item.Body, &body); err != nil {
			return "", fmt.Errorf("decode item %d: %w", item.ID, err)
		}

		eventIDs = append(eventIDs, body.EventID)
	}

	rows, err := db.New(r.h.opts.Pool).ListEventsByID(ctx, eventIDs)
	if err != nil {
		return "", fmt.Errorf("load the events: %w", err)
	}

	events := make([]event.Event, 0, len(rows))

	for _, row := range rows {
		ev, err := store.EventOf(row)
		if err != nil {
			return "", fmt.Errorf("read event %s: %w", row.ID, err)
		}

		events = append(events, ev)
	}

	return prompt.Inbox(events), nil
}

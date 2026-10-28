package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// The values of investigation_inbox.kind.
const (
	// InboxNewEvent is an event that joined the investigation's group.
	InboxNewEvent = "new_event"
	// InboxResolved is a recovery event of an alert in the group.
	InboxResolved = "resolved"
	// InboxWatcher is a change of a watcher's state, or a watcher that
	// did not start.
	InboxWatcher = watch.InboxKind
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

// inboxText writes the message that delivers items: the events, then the
// watchers. An item whose event no longer exists is left out of the text
// but still marked delivered.
func (r *run) inboxText(ctx context.Context, items []db.InvestigationInbox) (string, error) {
	var (
		eventIDs []uuid.UUID
		watchers []prompt.WatcherNote
	)

	for _, item := range items {
		switch item.Kind {
		case InboxNewEvent, InboxResolved:
			var body inboxEvent
			if err := json.Unmarshal(item.Body, &body); err != nil {
				return "", fmt.Errorf("decode item %d: %w", item.ID, err)
			}

			eventIDs = append(eventIDs, body.EventID)
		case InboxWatcher:
			var body watch.InboxItem
			if err := json.Unmarshal(item.Body, &body); err != nil {
				return "", fmt.Errorf("decode item %d: %w", item.ID, err)
			}

			watchers = append(watchers, prompt.WatcherNote{State: body.State, NotStarted: body.NotStarted})
		default:
			return "", fmt.Errorf("item %d: %w %q", item.ID, errUnknownInboxKind, item.Kind)
		}
	}

	var parts []string

	if len(eventIDs) > 0 {
		events, err := r.inboxEvents(ctx, eventIDs)
		if err != nil {
			return "", fmt.Errorf("load the events: %w", err)
		}

		parts = append(parts, prompt.Inbox(events))
	}

	if len(watchers) > 0 {
		parts = append(parts, prompt.Watchers(watchers))
	}

	return strings.Join(parts, "\n"), nil
}

// inboxEvents loads the events with ids that still exist.
func (r *run) inboxEvents(ctx context.Context, ids []uuid.UUID) ([]event.Event, error) {
	rows, err := db.New(r.h.opts.Pool).ListEventsByID(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list the events: %w", err)
	}

	events := make([]event.Event, 0, len(rows))

	for _, row := range rows {
		ev, err := store.EventOf(row)
		if err != nil {
			return nil, fmt.Errorf("read event %s: %w", row.ID, err)
		}

		events = append(events, ev)
	}

	return events, nil
}

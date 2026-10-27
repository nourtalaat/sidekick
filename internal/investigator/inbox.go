package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The values of investigation_inbox.kind.
const (
	// InboxNewEvent is an event that joined the investigation's group.
	InboxNewEvent = "new_event"
	// InboxResolved is a recovery event of an alert in the group.
	InboxResolved = "resolved"
	// InboxFollowUp is a follow-up question from a responder. It always
	// has a key.
	InboxFollowUp = "follow_up"
)

// followUpJobAttempts is how many failures make a follow_up job dead.
const followUpJobAttempts = 3

var (
	errUnknownInboxKind = errors.New("unknown inbox kind")
	errNoQuestionKey    = errors.New("a follow-up question needs a key")
)

// inboxEvent is the body of an inbox item about an event.
type inboxEvent struct {
	EventID uuid.UUID `json:"event_id"`
}

// Question is a follow-up question for AddFollowUp.
type Question struct {
	// Key tells questions apart, such as the Slack event ID of the reply.
	// A second question with the same key adds nothing.
	Key string
	// Author is the name the responder goes by, and AuthorID their ID,
	// such as a Slack user ID.
	Author   string
	AuthorID string
	// Text is the question, redacted already.
	Text string
}

// followUpBody is the body of an inbox item with a follow-up question.
type followUpBody struct {
	Text     string `json:"text"`
	Author   string `json:"author,omitempty"`
	AuthorID string `json:"author_id,omitempty"`
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

// FindInvestigation returns the investigation that one of sinks published
// at external, such as the Slack thread "channel:thread ts", and false when
// none did.
func FindInvestigation(ctx context.Context, q db.DBTX, sinks []string, external string) (uuid.UUID, bool, error) {
	if len(sinks) == 0 {
		return uuid.Nil, false, nil
	}

	id, err := db.New(q).FindInvestigationByExternal(ctx, db.FindInvestigationByExternalParams{Sinks: sinks, External: external})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}

	if err != nil {
		return uuid.Nil, false, fmt.Errorf("find the investigation at %s: %w", external, err)
	}

	return id, true, nil
}

// AddFollowUp adds a follow-up question to the inbox of the investigation
// with id. An investigation that has ended gets a follow_up job, which
// answers it in a new run; one that is running keeps it until its run
// ends, which then enqueues that job. Questions that arrive before the job
// starts share its run. It returns false, and adds nothing, for a question
// whose key was added before and for an investigation that takes no
// questions: one that failed or does not exist.
//
// The investigation's row stays locked until the question and the job are
// committed, so a run that ends meanwhile sees the question.
func AddFollowUp(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, q Question) (bool, error) {
	if q.Key == "" {
		return false, errNoQuestionKey
	}

	body, err := json.Marshal(followUpBody{Text: q.Text, Author: q.Author, AuthorID: q.AuthorID})
	if err != nil {
		return false, fmt.Errorf("encode the question: %w", err)
	}

	var added bool

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		inv, err := queries.LockInvestigation(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("lock the investigation: %w", err)
		}

		// TODO: [SK-19] Decide whether a cancelled investigation takes
		// follow-up questions; until then only these statuses do.
		if inv.Status != StatusQueued && inv.Status != StatusRunning && inv.Status != StatusFinal {
			return nil
		}

		rows, err := queries.InsertKeyedInboxItem(ctx, db.InsertKeyedInboxItemParams{
			InvestigationID: id,
			Kind:            InboxFollowUp,
			Body:            store.JSON(body),
			Key:             q.Key,
		})
		if err != nil {
			return fmt.Errorf("add the question: %w", err)
		}

		added = rows > 0

		if !added || inv.Status != StatusFinal {
			return nil
		}

		return enqueueFollowUp(ctx, tx, inv)
	})
	if err != nil {
		return false, fmt.Errorf("add a follow-up question to investigation %s: %w", id, err)
	}

	return added, nil
}

// deliverInbox saves every item that waits in the investigation's inbox
// and that the run takes as one inbox step, a user message the model sees
// at its next turn. The same transaction marks the items with the step's
// seq, so a resumed run never delivers them twice. It saves nothing when
// no item waits. The first run leaves follow-up questions waiting, for
// the follow-up run after it.
func (r *run) deliverInbox(ctx context.Context) error {
	queries := db.New(r.h.opts.Pool)

	waiting, err := queries.ListWaitingInbox(ctx, r.inv.ID)
	if err != nil {
		return fmt.Errorf("list the inbox: %w", err)
	}

	items := make([]db.InvestigationInbox, 0, len(waiting))

	for _, item := range waiting {
		if item.Kind != InboxFollowUp || r.followUp() {
			items = append(items, item)
		}
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
// follow-up questions. An item whose event no longer exists is left out of
// the text but still marked delivered.
func (r *run) inboxText(ctx context.Context, items []db.InvestigationInbox) (string, error) {
	var (
		eventIDs  []uuid.UUID
		questions []report.FollowUpQuestion
	)

	for _, item := range items {
		switch item.Kind {
		case InboxNewEvent, InboxResolved:
			var body inboxEvent
			if err := json.Unmarshal(item.Body, &body); err != nil {
				return "", fmt.Errorf("decode item %d: %w", item.ID, err)
			}

			eventIDs = append(eventIDs, body.EventID)
		case InboxFollowUp:
			q, err := questionOf(item)
			if err != nil {
				return "", fmt.Errorf("item %d: %w", item.ID, err)
			}

			questions = append(questions, q)
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

	if len(questions) > 0 {
		parts = append(parts, prompt.FollowUps(questions))
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

// enqueueWaitingFollowUp enqueues the follow_up job of inv when a
// follow-up question waits in its inbox. Call it in the transaction that
// ends a run, with the investigation's row locked by that run's update.
func enqueueWaitingFollowUp(ctx context.Context, q db.DBTX, inv db.Investigation) error {
	waiting, err := db.New(q).HasWaitingInbox(ctx, db.HasWaitingInboxParams{InvestigationID: inv.ID, Kind: InboxFollowUp})
	if err != nil {
		return fmt.Errorf("look for waiting follow-up questions: %w", err)
	}

	if !waiting {
		return nil
	}

	return enqueueFollowUp(ctx, q, inv)
}

// enqueueFollowUp enqueues the follow_up job of inv. A job that is pending
// or running for it already counts as enqueued.
func enqueueFollowUp(ctx context.Context, q db.DBTX, inv db.Investigation) error {
	payload, err := json.Marshal(Payload{InvestigationID: inv.ID, GroupID: inv.GroupID})
	if err != nil {
		return fmt.Errorf("encode the follow_up payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:        queue.KindFollowUp,
		Route:       inv.Route,
		Payload:     payload,
		UniqueKey:   FollowUpUniqueKey(inv.ID),
		MaxAttempts: followUpJobAttempts,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue follow_up: %w", err)
	}

	return nil
}

// questionOf reads the question of a follow-up inbox item.
func questionOf(item db.InvestigationInbox) (report.FollowUpQuestion, error) {
	var body followUpBody
	if err := json.Unmarshal(item.Body, &body); err != nil {
		return report.FollowUpQuestion{}, fmt.Errorf("decode the question: %w", err)
	}

	return report.FollowUpQuestion{Author: body.Author, Text: body.Text}, nil
}

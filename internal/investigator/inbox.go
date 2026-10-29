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

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
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
	// InboxFollowUp is a follow-up question from a responder. It always
	// has a key.
	InboxFollowUp = "follow_up"
	// InboxWatcher is a change of a watcher's state, or a watcher that
	// did not start.
	InboxWatcher = watch.InboxKind
)

// followUpJobAttempts is how many failures make a follow_up job dead.
const followUpJobAttempts = 3

var (
	errUnknownInboxKind = errors.New("unknown inbox kind")
	errNoQuestionKey    = errors.New("a follow-up question needs a key")
)

// ErrTriageOnly means the investigation ended with a triage-only result,
// which takes no follow-up questions until a full investigation has run.
var ErrTriageOnly = errors.New("the investigation has a triage-only result")

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
	// AuthorToken says AuthorID is the ID of an API token, for the audit
	// entry.
	AuthorToken bool
	// Text is the question, redacted already.
	Text string
}

// ActionFollowUpAsked is the action of the audit entry of a follow-up
// question that was added.
const ActionFollowUpAsked = "investigation.follow_up_asked"

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
// with id. An investigation that has ended with a report, final or
// stopped by a responder, gets a follow_up job, which answers it in a new
// run; one that is queued or running keeps it until its run
// ends, which then enqueues that job. Questions that arrive before the job
// starts share its run. It returns false, and adds nothing, for a question
// whose key was added before and for an investigation that takes no
// questions: one that failed or does not exist. A question to a
// triage-only result is not added either, and the error wraps
// ErrTriageOnly, so the asker can be told to ask for a full investigation
// first. A question that arrived while triage ran waits for the full
// investigation, if one is asked for.
//
// The investigation's row stays locked until the question and the job are
// committed, so a run that ends meanwhile sees the question.
func AddFollowUp(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, q Question) (bool, error) {
	var added bool

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error

		added, err = AddFollowUpTx(ctx, tx, id, q)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("add a follow-up question to investigation %s: %w", id, err)
	}

	return added, nil
}

// AddFollowUpTx is AddFollowUp in the caller's transaction tx, so the
// caller can write more in it, such as an audit row. The investigation's
// row stays locked until tx ends.
func AddFollowUpTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, q Question) (bool, error) {
	if q.Key == "" {
		return false, errNoQuestionKey
	}

	body, err := json.Marshal(followUpBody{Text: q.Text, Author: q.Author, AuthorID: q.AuthorID})
	if err != nil {
		return false, fmt.Errorf("encode the question: %w", err)
	}

	queries := db.New(tx)

	inv, err := queries.LockInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("lock the investigation: %w", err)
	}

	if inv.Status == StatusFailed {
		return false, nil
	}

	if triageOnlyResult(inv) {
		return false, ErrTriageOnly
	}

	rows, err := queries.InsertKeyedInboxItem(ctx, db.InsertKeyedInboxItemParams{
		InvestigationID: id,
		Kind:            InboxFollowUp,
		Body:            store.JSON(body),
		Key:             q.Key,
	})
	if err != nil {
		return false, fmt.Errorf("add the question: %w", err)
	}

	if rows == 0 {
		return false, nil
	}

	if answers(inv) {
		if err := enqueueFollowUp(ctx, tx, inv); err != nil {
			return false, fmt.Errorf("enqueue the follow-up job: %w", err)
		}
	}

	if err := auditQuestion(ctx, tx, id, q); err != nil {
		return false, err
	}

	return true, nil
}

// auditQuestion writes the audit entry of the question q added to the
// investigation with id. It records the question's key and size, never
// its text.
func auditQuestion(ctx context.Context, q db.DBTX, id uuid.UUID, question Question) error {
	kind := audit.ActorUser
	if question.AuthorToken {
		kind = audit.ActorToken
	}

	err := auditor.Write(ctx, q, audit.Entry{
		ActorKind:       kind,
		ActorID:         question.AuthorID,
		Action:          ActionFollowUpAsked,
		TargetKind:      auditTargetKind,
		TargetID:        id.String(),
		InvestigationID: &id,
		New:             map[string]any{"key": question.Key, "bytes": len(question.Text)},
	})
	if err != nil {
		return fmt.Errorf("audit the question to investigation %s: %w", id, err)
	}

	return nil
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
// watchers, then the follow-up questions. An item whose event no longer
// exists is left out of the text but still marked delivered.
func (r *run) inboxText(ctx context.Context, items []db.InvestigationInbox) (string, error) {
	var (
		eventIDs  []uuid.UUID
		watchers  []prompt.WatcherNote
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
		case InboxWatcher:
			var body watch.InboxItem
			if err := json.Unmarshal(item.Body, &body); err != nil {
				return "", fmt.Errorf("decode item %d: %w", item.ID, err)
			}

			watchers = append(watchers, prompt.WatcherNote{State: body.State, NotStarted: body.NotStarted})
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

	if len(watchers) > 0 {
		parts = append(parts, prompt.Watchers(watchers))
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

// answers reports whether inv answers follow-up questions now: it ended
// with a report, final or stopped by a responder, after a full
// investigation. A triage-only result has no conversation to go on from.
func answers(inv db.Investigation) bool {
	return (inv.Status == StatusFinal || inv.Status == StatusCancelled) && !triageOnlyResult(inv)
}

// triageOnlyResult reports whether inv ended with a triage-only result.
func triageOnlyResult(inv db.Investigation) bool {
	return inv.Status == StatusFinal && deref(inv.StopReason) == string(report.StopTriage)
}

// questionOf reads the question of a follow-up inbox item.
func questionOf(item db.InvestigationInbox) (report.FollowUpQuestion, error) {
	var body followUpBody
	if err := json.Unmarshal(item.Body, &body); err != nil {
		return report.FollowUpQuestion{}, fmt.Errorf("decode the question: %w", err)
	}

	return report.FollowUpQuestion{Author: body.Author, AuthorID: body.AuthorID, Text: body.Text}, nil
}

// Package publish shows each investigation in its route's outputs. It
// holds the publish job and the two calls that enqueue it: Open, which the
// router calls when it creates an investigation, and Enqueue, which the
// investigator calls with each new state.
//
// The newest state of an investigation, its publish state, is one row
// that every update replaces when its Seq is newer. A publish job carries
// only the investigation and the sink, and reads the state when it runs,
// so a job that waited always shows the newest state and the states in
// between are skipped. Its unique key, publish:<investigation>:<sink>,
// allows one pending or running job per message, so one message is never
// edited by two workers at once.
//
// A job that runs while a newer state is written must not lose it: the
// enqueue that wrote it found the key taken and added nothing. So at the
// end of a run the job locks the state row, stores what it published,
// gives up its unique key and, when the state is newer than what it showed,
// enqueues a new job, all in one transaction. Both sides take the locks in
// the same order, the state row first and then the job row, so they never
// wait for each other in a circle.
package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SeqQueued is the Seq of the queued state Open writes. Updates start
// above it.
const SeqQueued int64 = 0

// Concurrency is how many publish jobs one replica runs at once.
const Concurrency = 8

// jobMaxAttempts is how many failures make a publish job dead. With the
// queue's backoff it keeps trying through a Slack outage of about half an
// hour.
const jobMaxAttempts = 10

// uniqueKeyPrefix starts the unique key of a publish job.
const uniqueKeyPrefix = "publish:"

var (
	errSeqTooLow = errors.New("seq must be at least 1; the queued state has 0")
	errNoPhase   = errors.New("phase is required")
)

// Payload is the payload of a publish job.
type Payload struct {
	InvestigationID uuid.UUID `json:"investigation_id"`
	Sink            string    `json:"sink"`
}

// Update is one state of an investigation for its outputs.
type Update struct {
	InvestigationID uuid.UUID
	// Phase is the stage the state describes, such as output.PhaseRunning.
	Phase output.Phase
	// Seq orders the updates of one investigation: each must be higher
	// than the one before, starting at 1. An update whose Seq is not
	// higher than the stored one is dropped.
	Seq int64
	// Progress is every progress line so far, oldest first.
	Progress []output.Progress
	// Report is the result, set with output.PhaseFinal. A final update
	// without a report, or with StopReason report.StopError, shows that the
	// run could not finish.
	Report *report.Report
}

// body is how the progress and report of a state are stored.
type body struct {
	Progress []progressLine `json:"progress,omitempty"`
	Report   *report.Report `json:"report,omitempty"`
}

// progressLine is how one output.Progress is stored.
type progressLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// UniqueKey returns the unique key of the publish job of one investigation
// and sink.
func UniqueKey(investigationID uuid.UUID, sink string) string {
	return uniqueKeyPrefix + investigationID.String() + ":" + sink
}

// Open writes the queued state of a new investigation, with the sinks its
// updates go to, and enqueues a publish job for each sink, so the outputs
// show "Looking into it" at once. Call it in the transaction that creates
// the investigation. The sinks are fixed from then on, like the other
// settings of an investigation, and a second Open changes nothing.
func Open(ctx context.Context, q db.DBTX, investigationID uuid.UUID, sinks []string) error {
	if sinks == nil {
		// A nil slice would be stored as NULL, not as no sinks.
		sinks = []string{}
	}

	inserted, err := db.New(q).InsertPublishState(ctx, db.InsertPublishStateParams{
		InvestigationID: investigationID,
		Sinks:           sinks,
		Seq:             SeqQueued,
		Phase:           string(output.PhaseQueued),
		Body:            json.RawMessage(`{}`),
	})
	if err != nil {
		return fmt.Errorf("write the queued publish state: %w", err)
	}

	if inserted == 0 {
		return nil
	}

	return enqueueJobs(ctx, q, investigationID, sinks)
}

// Enqueue replaces the state of an investigation with u, when u.Seq is
// newer than the stored one, and enqueues a publish job for each of its
// sinks. A sink that has a job waiting already keeps that one, which will
// show u when it runs. An update that is not newer, or for an
// investigation that Open was not called for, changes nothing. Call it in
// the transaction that records what u shows.
//
// It locks the state row before it touches any job row, the same order a
// finishing publish job takes them in.
func Enqueue(ctx context.Context, q db.DBTX, u Update) error {
	if u.Seq <= SeqQueued {
		return errSeqTooLow
	}

	if u.Phase == "" {
		return errNoPhase
	}

	stored := body{Report: u.Report}
	for _, p := range u.Progress {
		stored.Progress = append(stored.Progress, progressLine(p))
	}

	raw, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode the publish state: %w", err)
	}

	sinks, err := db.New(q).UpdatePublishState(ctx, db.UpdatePublishStateParams{
		InvestigationID: u.InvestigationID,
		Seq:             u.Seq,
		Phase:           string(u.Phase),
		Body:            raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("write the publish state: %w", err)
	}

	return enqueueJobs(ctx, q, u.InvestigationID, sinks)
}

// enqueueJobs adds the publish job of each sink. A job that is already
// pending or running for a sink counts as added.
func enqueueJobs(ctx context.Context, q db.DBTX, investigationID uuid.UUID, sinks []string) error {
	for _, sink := range sinks {
		if err := enqueueJob(ctx, q, Payload{InvestigationID: investigationID, Sink: sink}); err != nil {
			return fmt.Errorf("sink %q: %w", sink, err)
		}
	}

	return nil
}

// enqueueJob adds the publish job of one investigation and sink.
func enqueueJob(ctx context.Context, q db.DBTX, p Payload) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode the publish payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:        queue.KindPublish,
		Payload:     payload,
		UniqueKey:   UniqueKey(p.InvestigationID, p.Sink),
		MaxAttempts: jobMaxAttempts,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue publish: %w", err)
	}

	return nil
}

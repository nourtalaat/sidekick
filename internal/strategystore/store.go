// Package strategystore keeps the strategies people edit at runtime. Every
// save is a new version, stored with its author and reason and never
// changed after, so an investigation can name the exact version it used.
// Save is the one way to add a version. The Cache holds the strategies in
// effect on one replica and follows the changes the other replicas make.
package strategystore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// The audit actions of strategy saves, and the kind of what they change.
const (
	ActionCreated = "strategy.created"
	ActionUpdated = "strategy.updated"
	KindStrategy  = "strategy"
)

// MaxReasonLength is the most characters a reason can have.
const MaxReasonLength = 500

// uniqueViolation is the Postgres error code of a duplicate key.
const uniqueViolation = "23505"

var (
	// ErrStale means the strategy was saved again since the version the
	// caller read, so the caller's copy is out of date.
	ErrStale = errors.New("the strategy was saved again since it was read")
	// ErrUnchanged means the body is the same as the current version.
	ErrUnchanged = errors.New("the body is the same as the current version")
	// ErrConflict means another save created the same strategy at the same
	// moment. Trying again works.
	ErrConflict = errors.New("the strategy was created at the same moment by someone else")
	// ErrNotFound means no runtime strategy or version has that name.
	ErrNotFound = errors.New("no runtime strategy has that name and version")
)

// InvalidError is the error of a save that is refused because the reason or
// the body does not pass the checks.
type InvalidError struct {
	// Reason says what is wrong with the reason, or is empty.
	Reason string
	// Body lists the problems of the body, with the path and line of each.
	Body []strategy.FieldProblem
}

// Error implements error.
func (e *InvalidError) Error() string {
	var lines []string

	if e.Reason != "" {
		lines = append(lines, "reason: "+e.Reason)
	}

	for _, p := range e.Body {
		lines = append(lines, p.String())
	}

	return "the strategy is not valid:\n" + strings.Join(lines, "\n")
}

// Actor is who saves a version. The audit log records it, and the author of
// the version is its ID when that is a UUID, as it is for a web UI user and
// an API token.
type Actor struct {
	// Kind is audit.ActorUser or audit.ActorToken.
	Kind string
	// ID identifies the user or token.
	ID string
}

// SaveInput is what Save needs to add a version.
type SaveInput struct {
	Actor Actor
	// Name is the strategy; the name in Body must be the same.
	Name string
	// Body is the strategy as YAML, in the format of a strategy file.
	Body string
	// Reason says why the version is saved. It is required.
	Reason string
	// BaseVersion is the version the caller read, or nil to skip the
	// check. A different current version means someone saved since, and
	// the save fails with ErrStale. Zero means the caller read no
	// version, so the strategy must not exist yet.
	BaseVersion *int
	// ProposalID and BenchRunID are set when the version comes from a
	// strategy proposal and the benchmark run that checked it.
	ProposalID, BenchRunID *uuid.UUID
}

// Version is one stored version of a runtime strategy.
type Version struct {
	Name   string
	Number int
	// Body is the YAML. Lists of versions leave it empty.
	Body string
	// Author is the user or token that saved the version, if known, and
	// AuthorName its name when it still exists.
	Author     *uuid.UUID
	AuthorName string
	Reason     string
	CreatedAt  time.Time
	ProposalID *uuid.UUID
	BenchRunID *uuid.UUID
}

// Saved is the result of Save.
type Saved struct {
	// Created is true when this is the first version of the strategy.
	Created bool
	Version Version
	// Strategy is the strategy as the version holds it.
	Strategy strategy.Strategy
}

// Store saves runtime strategies and reads them back. Each save is checked
// and written in one transaction with its audit entry and the
// strategies_changed notification. It is safe for concurrent use.
type Store struct {
	pool  *pgxpool.Pool
	audit *audit.Writer
	now   func() time.Time
}

// NewStore returns a Store on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, audit: audit.NewWriter(), now: time.Now}
}

// Save adds a version of the strategy in.Name, creating the strategy with
// version 1 when it does not exist. The body and the reason are checked
// first: the error is an *InvalidError when either fails, with the path of
// each problem in the body. A saved version never changes, and the
// replicas use it once they reload. The error wraps ErrStale when
// in.BaseVersion is not the current version, ErrUnchanged when the body is
// the current one, and ErrConflict when a save of a new strategy of the
// same name ran at the same moment.
func (s *Store) Save(ctx context.Context, in SaveInput) (Saved, error) {
	st, err := check(in)
	if err != nil {
		return Saved{}, fmt.Errorf("check strategy %s: %w", in.Name, err)
	}

	var saved Saved

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error

		saved, err = s.add(ctx, tx, in, st)
		if err != nil {
			return fmt.Errorf("add the version: %w", err)
		}

		return nil
	})

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Saved{}, fmt.Errorf("save strategy %s: %w: %w", in.Name, ErrConflict, err)
	}

	if err != nil {
		return Saved{}, fmt.Errorf("save strategy %s: %w", in.Name, err)
	}

	return saved, nil
}

// add writes the version, its audit entry and the notification.
func (s *Store) add(ctx context.Context, tx pgx.Tx, in SaveInput, st strategy.Strategy) (Saved, error) {
	q := db.New(tx)

	current, created, err := s.lock(ctx, q, in.Name)
	if err != nil {
		return Saved{}, fmt.Errorf("lock strategy: %w", err)
	}

	if in.BaseVersion != nil && *in.BaseVersion != current {
		return Saved{}, ErrStale
	}

	if !created {
		old, err := q.GetStrategyVersion(ctx, db.GetStrategyVersionParams{Name: in.Name, Version: int32(current)}) //nolint:gosec // Versions count saves, which stay far below the limit.
		if err != nil {
			return Saved{}, fmt.Errorf("read current version: %w", err)
		}

		if old.Body == in.Body {
			return Saved{}, ErrUnchanged
		}
	}

	v := Version{
		Name: in.Name, Number: current + 1, Body: in.Body, Author: authorOf(in.Actor), Reason: in.Reason,
		CreatedAt: s.now(), ProposalID: in.ProposalID, BenchRunID: in.BenchRunID,
	}

	if err := s.write(ctx, q, v, created); err != nil {
		return Saved{}, fmt.Errorf("write version %d: %w", v.Number, err)
	}

	if err := s.record(ctx, tx, in.Actor, v, created); err != nil {
		return Saved{}, fmt.Errorf("record version %d: %w", v.Number, err)
	}

	st.Version = strategy.RuntimeVersion(v.Number)

	return Saved{Created: created, Version: v, Strategy: st}, nil
}

// lock returns the current version of the strategy and locks it, or zero
// and true when the strategy does not exist yet.
func (s *Store) lock(ctx context.Context, q *db.Queries, name string) (current int, created bool, err error) {
	row, err := q.LockStrategy(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, true, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("read strategy: %w", err)
	}

	return int(row.CurrentVersion), false, nil
}

// write inserts the version, and the strategy or its new current version.
func (s *Store) write(ctx context.Context, q *db.Queries, v Version, created bool) error {
	if created {
		if err := q.InsertStrategy(ctx, db.InsertStrategyParams{Name: v.Name, CreatedAt: v.CreatedAt}); err != nil {
			return fmt.Errorf("insert strategy: %w", err)
		}
	}

	err := q.InsertStrategyVersion(ctx, db.InsertStrategyVersionParams{
		Name: v.Name, Version: int32(v.Number), Body: v.Body, Author: v.Author, Reason: v.Reason, //nolint:gosec // Versions count saves, which stay far below the limit.
		CreatedAt: v.CreatedAt, ProposalID: v.ProposalID, BenchRunID: v.BenchRunID,
	})
	if err != nil {
		return fmt.Errorf("insert version: %w", err)
	}

	if created {
		return nil
	}

	err = q.SetStrategyCurrentVersion(ctx, db.SetStrategyCurrentVersionParams{Name: v.Name, CurrentVersion: int32(v.Number)}) //nolint:gosec // Versions count saves, which stay far below the limit.
	if err != nil {
		return fmt.Errorf("set current version: %w", err)
	}

	return nil
}

// auditView is how the audit log shows a version: what it was and why, not
// the body, which the version table keeps.
type auditView struct {
	Version    int    `json:"version"`
	Reason     string `json:"reason,omitempty"`
	BodySHA256 string `json:"bodySha256,omitempty"`
}

// record writes the audit entry of a save and the notification that makes
// every replica reload.
func (s *Store) record(ctx context.Context, tx pgx.Tx, actor Actor, v Version, created bool) error {
	sum := sha256.Sum256([]byte(v.Body))
	action, old := ActionUpdated, any(auditView{Version: v.Number - 1})

	if created {
		action, old = ActionCreated, nil
	}

	err := s.audit.Write(ctx, tx, audit.Entry{
		ActorKind: actor.Kind, ActorID: actor.ID, Action: action, TargetKind: KindStrategy, TargetID: v.Name,
		Old: old, New: auditView{Version: v.Number, Reason: v.Reason, BodySHA256: hex.EncodeToString(sum[:])},
	})
	if err != nil {
		return fmt.Errorf("audit the save: %w", err)
	}

	if err := db.New(tx).NotifyStrategiesChanged(ctx, v.Name); err != nil {
		return fmt.Errorf("notify strategies_changed: %w", err)
	}

	return nil
}

// check validates the reason and the body of a save, and returns the
// strategy the body holds.
func check(in SaveInput) (strategy.Strategy, error) {
	invalid := &InvalidError{}

	switch reason := strings.TrimSpace(in.Reason); {
	case reason == "":
		invalid.Reason = "a reason is required"
	case utf8.RuneCountInString(reason) > MaxReasonLength:
		invalid.Reason = fmt.Sprintf("the reason is longer than %d characters", MaxReasonLength)
	}

	st, problems := strategy.Check([]byte(in.Body))
	invalid.Body = problems

	if len(problems) == 0 && st.Name != in.Name {
		invalid.Body = []strategy.FieldProblem{{Path: "name", Message: fmt.Sprintf("must be %q, the name of the strategy it is saved to", in.Name)}}
	}

	if invalid.Reason != "" || len(invalid.Body) > 0 {
		return strategy.Strategy{}, invalid
	}

	return st, nil
}

// authorOf returns the ID of actor as a UUID, or nil when it is not one.
func authorOf(actor Actor) *uuid.UUID {
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil
	}

	return &id
}

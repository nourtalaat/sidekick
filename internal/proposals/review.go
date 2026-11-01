package proposals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// MaxBodyBytes is the most bytes the body of a proposal may have, whether a
// model drafted it or an admin edited it.
const MaxBodyBytes = 32 << 10

// MaxReasonChars is the most characters of a reason an admin gives when
// rejecting.
const MaxReasonChars = 500

// gateResultPassed is the gate_result of a proposal that passed.
const gateResultPassed = "passed"

var (
	// ErrNotFound means there is no proposal with the ID.
	ErrNotFound = errors.New("there is no strategy proposal with that ID")
	// ErrNotPassed means the proposal has not passed its gate, or has
	// already been decided on, so it cannot be approved. Nothing can make a
	// proposal that did not pass applicable, and there is no override.
	ErrNotPassed = errors.New("the strategy proposal has not passed its benchmark gate, or was decided on already")
	// ErrNotEditable means the proposal is not at a state where its body can
	// be edited: only a proposal whose gate has ended and that nobody has
	// decided on can.
	ErrNotEditable = errors.New("the strategy proposal cannot be edited now")
	// ErrNotRejectable means the proposal was decided on already, or has no
	// draft yet.
	ErrNotRejectable = errors.New("the strategy proposal cannot be rejected now")
	// ErrBadBase means a proposal's base version is not one a version can be
	// saved on top of.
	ErrBadBase = errors.New("the strategy proposal's base version is not valid")
)

// Service reviews proposals: approve, reject and edit. Every change is
// written with its audit entry in one transaction.
type Service struct {
	pool  *pgxpool.Pool
	store *strategystore.Store
	audit *audit.Writer
}

// NewService returns a Service. store makes the version of an approved
// proposal.
func NewService(pool *pgxpool.Pool, store *strategystore.Store) *Service {
	return &Service{pool: pool, store: store, audit: audit.NewWriter()}
}

// Approved is the result of Approve.
type Approved struct {
	Proposal db.StrategyProposal
	// Version is the version that was saved, for a runtime or built-in
	// strategy.
	Version *strategystore.Version
	// YAML is the new strategy for an admin to commit, for a file strategy,
	// which Sidekick cannot change.
	YAML string
}

// Approve applies a proposal that passed its gate. For a runtime strategy it
// saves the proposal's body as the next version; for a built-in one it saves
// a runtime strategy of the same name, which takes its place; for a file
// strategy it changes nothing and returns the YAML. The decision and the
// version are saved together, and the version records the proposal and the
// benchmark run that checked it.
//
// The error wraps ErrNotFound, ErrNotPassed for a proposal whose gate did not
// pass or that was decided on, strategystore.ErrStale when the strategy was
// saved again since the proposal was drafted, and strategystore.ErrUnchanged
// when the strategy already holds the body.
func (s *Service) Approve(ctx context.Context, id uuid.UUID, by strategystore.Actor) (Approved, error) {
	var out Approved

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("find the proposal: %w", err)
		}

		if p.Status != StatusPassed || p.GateResult == nil || *p.GateResult != gateResultPassed {
			return fmt.Errorf("proposal %s is %s: %w", id, p.Status, ErrNotPassed)
		}

		out, err = s.apply(ctx, tx, p, by)
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}

		return s.audit.Write(ctx, tx, audit.Entry{
			ActorKind: by.Kind, ActorID: by.ID, Action: ActionApproved, TargetKind: KindProposal, TargetID: id.String(),
			Old: map[string]any{"status": p.Status}, New: approvedView(out),
		})
	})
	if err != nil {
		return Approved{}, fmt.Errorf("approve proposal %s: %w", id, err)
	}

	return out, nil
}

// Reject sets a proposal that has a draft and has not been decided on aside. A
// gate that is still running stops before its next run.
func (s *Service) Reject(ctx context.Context, id uuid.UUID, by strategystore.Actor, reason string) (db.StrategyProposal, error) {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > MaxReasonChars {
		return db.StrategyProposal{}, &strategystore.InvalidError{Reason: fmt.Sprintf("the reason is longer than %d characters", MaxReasonChars)}
	}

	var out db.StrategyProposal

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("find the proposal: %w", err)
		}

		switch p.Status {
		case StatusPending, StatusBenchmarking, StatusPassed, StatusFailed:
		default:
			return fmt.Errorf("proposal %s is %s: %w", id, p.Status, ErrNotRejectable)
		}

		q := db.New(tx)

		if _, err := q.SetStrategyProposalDecision(ctx, db.SetStrategyProposalDecisionParams{ID: id, Status: StatusRejected, Reviewer: reviewerID(by)}); err != nil {
			return fmt.Errorf("reject: %w", err)
		}

		if err := q.DeleteStrategyGateRuns(ctx, id); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}

		out, err = q.GetStrategyProposal(ctx, id)
		if err != nil {
			return fmt.Errorf("read the proposal: %w", err)
		}

		return s.audit.Write(ctx, tx, audit.Entry{
			ActorKind: by.Kind, ActorID: by.ID, Action: ActionRejected, TargetKind: KindProposal, TargetID: id.String(),
			Old: map[string]any{"status": p.Status}, New: map[string]any{"status": StatusRejected, "reason": reason},
		})
	})
	if err != nil {
		return db.StrategyProposal{}, fmt.Errorf("reject proposal %s: %w", id, err)
	}

	return out, nil
}

// Edit replaces the body of a proposal whose gate has ended and that nobody
// has decided on, and sends it through the gate again. The body is checked as
// a saved strategy is: the error is a *strategystore.InvalidError with the
// path of each problem. The old benchmark results are dropped from the
// proposal, which keeps no result that does not match its body.
func (s *Service) Edit(ctx context.Context, id uuid.UUID, by strategystore.Actor, body string) (db.StrategyProposal, error) {
	var out db.StrategyProposal

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("find the proposal: %w", err)
		}

		if p.Status != StatusPassed && p.Status != StatusFailed {
			return fmt.Errorf("proposal %s is %s: %w", id, p.Status, ErrNotEditable)
		}

		if err := CheckBody(body, p.Name); err != nil {
			return fmt.Errorf("check the body: %w", err)
		}

		q := db.New(tx)

		if _, err := q.ResetStrategyProposalForEdit(ctx, db.ResetStrategyProposalForEditParams{ID: id, Body: &body, Reviewer: reviewerID(by)}); err != nil {
			return fmt.Errorf("replace the body: %w", err)
		}

		if err := q.DeleteStrategyGateRuns(ctx, id); err != nil {
			return fmt.Errorf("delete the kept runs: %w", err)
		}

		if err := enqueueGate(ctx, tx, id); err != nil {
			return fmt.Errorf("queue the gate: %w", err)
		}

		out, err = q.GetStrategyProposal(ctx, id)
		if err != nil {
			return fmt.Errorf("read the proposal: %w", err)
		}

		return s.audit.Write(ctx, tx, audit.Entry{
			ActorKind: by.Kind, ActorID: by.ID, Action: ActionEdited, TargetKind: KindProposal, TargetID: id.String(),
			Old: bodyView(p.Body), New: bodyView(&body),
		})
	})
	if err != nil {
		return db.StrategyProposal{}, fmt.Errorf("edit proposal %s: %w", id, err)
	}

	return out, nil
}

// CheckBody checks the body of a proposal the way a saved strategy is
// checked: it must be a valid strategy file for the strategy called name, and
// not too large. The error is a *strategystore.InvalidError.
func CheckBody(body, name string) error {
	if len(body) > MaxBodyBytes {
		return &strategystore.InvalidError{Body: []strategy.FieldProblem{{Message: fmt.Sprintf("the strategy is longer than %d bytes", MaxBodyBytes)}}}
	}

	st, problems := strategy.Check([]byte(body))
	if len(problems) > 0 {
		return &strategystore.InvalidError{Body: problems}
	}

	if st.Name != name {
		return &strategystore.InvalidError{Body: []strategy.FieldProblem{{Path: "name", Message: fmt.Sprintf("must be %q, the name of the strategy the proposal changes", name)}}}
	}

	return nil
}

// apply makes the decision of an approval: a saved version, or the YAML of a
// file strategy.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, p db.StrategyProposal, by strategystore.Actor) (Approved, error) {
	q := db.New(tx)

	if p.Body == nil || p.BaseOrigin == nil {
		return Approved{}, fmt.Errorf("proposal %s has no draft: %w", p.ID, ErrNotPassed)
	}

	if strategy.Origin(*p.BaseOrigin) == strategy.File {
		if _, err := q.SetStrategyProposalDecision(ctx, db.SetStrategyProposalDecisionParams{ID: p.ID, Status: StatusApproved, Reviewer: reviewerID(by)}); err != nil {
			return Approved{}, fmt.Errorf("approve: %w", err)
		}

		decided, err := q.GetStrategyProposal(ctx, p.ID)
		if err != nil {
			return Approved{}, fmt.Errorf("read the proposal: %w", err)
		}

		return Approved{Proposal: decided, YAML: *p.Body}, nil
	}

	base, err := baseVersion(p)
	if err != nil {
		return Approved{}, err
	}

	saved, err := s.store.SaveIn(ctx, tx, strategystore.SaveInput{
		Actor: by, Name: p.Name, Body: *p.Body, Reason: reasonOf(p), BaseVersion: &base, ProposalID: &p.ID, BenchRunID: p.BenchRunID,
	})
	if err != nil {
		return Approved{}, fmt.Errorf("save the version: %w", err)
	}

	if _, err := q.SetStrategyProposalDecision(ctx, db.SetStrategyProposalDecisionParams{ID: p.ID, Status: StatusApplied, Reviewer: reviewerID(by)}); err != nil {
		return Approved{}, fmt.Errorf("apply: %w", err)
	}

	decided, err := q.GetStrategyProposal(ctx, p.ID)
	if err != nil {
		return Approved{}, fmt.Errorf("read the proposal: %w", err)
	}

	return Approved{Proposal: decided, Version: &saved.Version}, nil
}

// baseVersion returns the version number a save for the proposal builds on:
// the runtime version it was drafted from, or zero for a built-in strategy,
// which has no runtime version yet.
func baseVersion(p db.StrategyProposal) (int, error) {
	if strategy.Origin(*p.BaseOrigin) == strategy.BuiltIn {
		return 0, nil
	}

	n, ok := strategy.RuntimeVersionNumber(p.BaseVersion)
	if !ok {
		return 0, fmt.Errorf("proposal %s has base version %q: %w", p.ID, p.BaseVersion, ErrBadBase)
	}

	return n, nil
}

// reasonOf returns the reason of the version an approval saves: the proposal
// and its rationale, cut to the longest reason a save takes.
func reasonOf(p db.StrategyProposal) string {
	reason := "Proposal " + p.ID.String()
	if p.Rationale != nil {
		reason += ": " + strings.TrimSpace(*p.Rationale)
	}

	if utf8.RuneCountInString(reason) <= strategystore.MaxReasonLength {
		return reason
	}

	return string([]rune(reason)[:strategystore.MaxReasonLength-1]) + "…"
}

// lock reads a proposal and locks it until the transaction ends.
func lock(ctx context.Context, tx pgx.Tx, id uuid.UUID) (db.StrategyProposal, error) {
	p, err := db.New(tx).LockStrategyProposal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.StrategyProposal{}, fmt.Errorf("proposal %s: %w", id, ErrNotFound)
	}

	if err != nil {
		return db.StrategyProposal{}, fmt.Errorf("lock proposal %s: %w", id, err)
	}

	return p, nil
}

// enqueueGate queues the bench_gate job of a proposal.
func enqueueGate(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	payload, err := json.Marshal(BenchGatePayload{ProposalID: id})
	if err != nil {
		return fmt.Errorf("encode the payload of the gate job: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{Kind: queue.KindBenchGate, Payload: payload, UniqueKey: BenchGateKey(id)})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("queue the gate of proposal %s: %w", id, err)
	}

	return nil
}

// reviewerID returns the ID of the user or token that acts, or nil when it is
// not a UUID.
func reviewerID(by strategystore.Actor) *uuid.UUID {
	id, err := uuid.Parse(by.ID)
	if err != nil {
		return nil
	}

	return &id
}

// approvedView is how the audit log shows an approval.
func approvedView(a Approved) map[string]any {
	view := map[string]any{"status": a.Proposal.Status}
	if a.Version != nil {
		view["version"] = a.Version.Number
	}

	return view
}

// bodyView is how the audit log shows a body: its hash, not the text, which
// the proposal holds.
func bodyView(body *string) map[string]any {
	if body == nil {
		return nil
	}

	sum := sha256.Sum256([]byte(*body))

	return map[string]any{"bodySha256": hex.EncodeToString(sum[:])}
}

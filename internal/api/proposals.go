package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/gate"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// The page sizes of GET /strategy-proposals.
const (
	defaultProposalPage = 50
	maxProposalPage     = 200
)

// The codes of the problems that proposal reviews answer with, besides those
// of strategy saves.
const codeNotPassed = "not_passed"

// errProposalsUnavailable answers a request when the server was started
// without a proposal reviewer.
var errProposalsUnavailable = problemf(http.StatusServiceUnavailable, "Strategy proposals are not available.")

// errNoProposal answers a request for a proposal that does not exist.
var errNoProposal = &Error{Status: http.StatusNotFound, Code: codeNotFound, Detail: "No strategy proposal has this ID."}

// ProposalReviewer approves, rejects and edits strategy proposals.
// *proposals.Service is one.
type ProposalReviewer interface {
	Approve(ctx context.Context, id uuid.UUID, by strategystore.Actor) (proposals.Approved, error)
	Reject(ctx context.Context, id uuid.UUID, by strategystore.Actor, reason string) (db.StrategyProposal, error)
	Edit(ctx context.Context, id uuid.UUID, by strategystore.Actor, body string) (db.StrategyProposal, error)
}

// ListStrategyProposals returns a page of proposals, newest first.
func (s *server) ListStrategyProposals(ctx context.Context, req apigen.ListStrategyProposalsRequestObject) (apigen.ListStrategyProposalsResponseObject, error) {
	params, limit, err := proposalParams(req.Params)
	if err != nil {
		return nil, err
	}

	rows, err := db.New(store.CustomPlan{DB: s.pool}).ListStrategyProposals(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list strategy proposals: %w", err)
	}

	list := apigen.ListStrategyProposals200JSONResponse{Proposals: make([]apigen.StrategyProposalSummary, 0, min(len(rows), limit))}

	for i, row := range rows {
		if i == limit {
			cursor := investigationCursor{createdAt: rows[i-1].CreatedAt, id: rows[i-1].ID}.String()
			list.NextCursor = &cursor

			break
		}

		list.Proposals = append(list.Proposals, proposalSummaryOf(proposalFields{
			id: row.ID, name: row.Name, baseVersion: row.BaseVersion, baseOrigin: row.BaseOrigin, rationale: row.Rationale,
			triggerCount: row.TriggerCount, status: row.Status, statusDetail: row.StatusDetail, gateResult: row.GateResult,
			benchRunID: row.BenchRunID, baselineRunID: row.BaselineRunID, reviewer: row.Reviewer,
			createdAt: row.CreatedAt, updatedAt: row.UpdatedAt, decidedAt: row.DecidedAt,
		}))
	}

	return list, nil
}

// GetStrategyProposal returns one proposal with its YAML, its feedback and
// its gate result.
func (s *server) GetStrategyProposal(ctx context.Context, req apigen.GetStrategyProposalRequestObject) (apigen.GetStrategyProposalResponseObject, error) {
	p, err := s.proposalDetail(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	return apigen.GetStrategyProposal200JSONResponse(p), nil
}

// EditStrategyProposal replaces the YAML of a proposal and sends it through
// the gate again.
func (s *server) EditStrategyProposal(ctx context.Context, req apigen.EditStrategyProposalRequestObject) (apigen.EditStrategyProposalResponseObject, error) {
	if s.proposalReviewer == nil {
		return nil, errProposalsUnavailable
	}

	if req.Body == nil {
		return nil, problemf(http.StatusBadRequest, "Send the new YAML as body.")
	}

	if _, err := s.proposalReviewer.Edit(ctx, req.Id, proposalActor(ctx), req.Body.Body); err != nil {
		return nil, proposalError(err)
	}

	p, err := s.proposalDetail(ctx, req.Id)
	if err != nil {
		return nil, err
	}

	return apigen.EditStrategyProposal200JSONResponse(p), nil
}

// ApproveStrategyProposal applies a proposal that passed its gate.
func (s *server) ApproveStrategyProposal(ctx context.Context, req apigen.ApproveStrategyProposalRequestObject) (apigen.ApproveStrategyProposalResponseObject, error) {
	if s.proposalReviewer == nil {
		return nil, errProposalsUnavailable
	}

	got, err := s.proposalReviewer.Approve(ctx, req.Id, proposalActor(ctx))
	if err != nil {
		return nil, proposalError(err)
	}

	out := apigen.ApproveStrategyProposal200JSONResponse{Proposal: proposalSummaryOf(fieldsOf(got.Proposal)), Yaml: nonEmptyText(got.YAML)}
	if got.Version != nil {
		out.Version = &got.Version.Number
	}

	return out, nil
}

// RejectStrategyProposal sets a proposal aside.
func (s *server) RejectStrategyProposal(ctx context.Context, req apigen.RejectStrategyProposalRequestObject) (apigen.RejectStrategyProposalResponseObject, error) {
	if s.proposalReviewer == nil {
		return nil, errProposalsUnavailable
	}

	var reason string
	if req.Body != nil {
		reason = textOf(req.Body.Reason)
	}

	p, err := s.proposalReviewer.Reject(ctx, req.Id, proposalActor(ctx), reason)
	if err != nil {
		return nil, proposalError(err)
	}

	return apigen.RejectStrategyProposal200JSONResponse(proposalSummaryOf(fieldsOf(p))), nil
}

// proposalDetail reads a proposal with the feedback it was drafted from and
// its gate result.
func (s *server) proposalDetail(ctx context.Context, id uuid.UUID) (apigen.StrategyProposal, error) {
	q := db.New(s.pool)

	p, err := q.GetStrategyProposal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return apigen.StrategyProposal{}, errNoProposal
	}

	if err != nil {
		return apigen.StrategyProposal{}, fmt.Errorf("read strategy proposal %s: %w", id, err)
	}

	rows, err := q.ListFeedbackByIDs(ctx, p.FeedbackIds)
	if err != nil {
		return apigen.StrategyProposal{}, fmt.Errorf("list the feedback of strategy proposal %s: %w", id, err)
	}

	stale, err := s.proposalStale(ctx, p)
	if err != nil {
		return apigen.StrategyProposal{}, fmt.Errorf("check strategy proposal %s for staleness: %w", id, err)
	}

	gateView, hasGate, err := gateOf(p)
	if err != nil {
		return apigen.StrategyProposal{}, fmt.Errorf("strategy proposal %s: %w", id, err)
	}

	summary := proposalSummaryOf(fieldsOf(p))

	out := apigen.StrategyProposal{
		Id: summary.Id, Name: summary.Name, BaseVersion: summary.BaseVersion, BaseOrigin: summary.BaseOrigin, Rationale: summary.Rationale,
		TriggerCount: summary.TriggerCount, Status: summary.Status, StatusDetail: summary.StatusDetail, GateResult: summary.GateResult,
		BenchRunId: summary.BenchRunId, BaselineRunId: summary.BaselineRunId, Reviewer: summary.Reviewer,
		CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt, DecidedAt: summary.DecidedAt,
		BaseBody: p.BaseBody, Body: p.Body, Stale: stale, Approvable: p.Status == proposals.StatusPassed,
		Feedback: make([]apigen.StrategyProposalFeedback, 0, len(rows)),
	}

	if hasGate {
		out.Gate = &gateView
	}

	for _, r := range rows {
		out.Feedback = append(out.Feedback, apigen.StrategyProposalFeedback{
			Id: r.ID, InvestigationId: r.InvestigationID, TargetKind: r.TargetKind, TargetRef: r.TargetRef, Rating: r.Rating,
			Reason: r.Reason, CorrectVerdict: r.CorrectVerdict, CorrectCause: r.CorrectCause, UpdatedAt: r.UpdatedAt,
		})
	}

	return out, nil
}

// proposalStale reports whether the strategy in effect is no longer at the
// version the proposal was drafted from. Without a strategy store it is
// not known, and the answer is false.
func (s *server) proposalStale(ctx context.Context, p db.StrategyProposal) (bool, error) {
	if s.strategies == nil {
		return false, nil
	}

	o, err := s.strategyOverview(ctx)
	if err != nil {
		return false, fmt.Errorf("read the strategies: %w", err)
	}

	st, ok := o.effective.Set.Get(p.Name)

	return !ok || st.Version != p.BaseVersion, nil
}

// gateOf returns the stored result of a proposal's gate, and false when the
// gate has not ended.
func gateOf(p db.StrategyProposal) (apigen.StrategyGate, bool, error) {
	if len(p.GateDetail) == 0 {
		return apigen.StrategyGate{}, false, nil
	}

	var d gate.Detail
	if err := json.Unmarshal(p.GateDetail, &d); err != nil {
		return apigen.StrategyGate{}, false, fmt.Errorf("decode the gate detail: %w", err)
	}

	out := apigen.StrategyGate{
		Result: apigen.StrategyGateResult(d.Result), Reason: d.Reason, Mean: d.Mean,
		Interval: apigen.BenchInterval{Lower: d.Interval.Lower, Upper: d.Interval.Upper},
		MaxDrop:  d.MaxDrop, Resamples: d.Resamples, Seed: strconv.FormatUint(d.Seed, 10), Repeats: d.Repeats,
		CostMicros: d.CostMicros, MaxCostMicros: d.MaxCostMicros, Error: nonEmptyText(d.Error),
		Scenarios: make([]apigen.StrategyGateScenario, 0, len(d.Scenarios)),
	}

	for _, sc := range d.Scenarios {
		out.Scenarios = append(out.Scenarios, apigen.StrategyGateScenario{Id: sc.ID, Baseline: sc.Baseline, Proposal: sc.Proposal, Difference: sc.Difference})
	}

	return out, true, nil
}

// proposalFields are the fields the summary of a proposal shows, whichever
// query read them.
type proposalFields struct {
	id            uuid.UUID
	name          string
	baseVersion   string
	baseOrigin    *string
	rationale     *string
	triggerCount  int32
	status        string
	statusDetail  *string
	gateResult    *string
	benchRunID    *uuid.UUID
	baselineRunID *uuid.UUID
	reviewer      *uuid.UUID
	createdAt     time.Time
	updatedAt     time.Time
	decidedAt     *time.Time
}

// fieldsOf returns the summary fields of a whole proposal.
func fieldsOf(p db.StrategyProposal) proposalFields {
	return proposalFields{
		id: p.ID, name: p.Name, baseVersion: p.BaseVersion, baseOrigin: p.BaseOrigin, rationale: p.Rationale, triggerCount: p.TriggerCount,
		status: p.Status, statusDetail: p.StatusDetail, gateResult: p.GateResult, benchRunID: p.BenchRunID, baselineRunID: p.BaselineRunID,
		reviewer: p.Reviewer, createdAt: p.CreatedAt, updatedAt: p.UpdatedAt, decidedAt: p.DecidedAt,
	}
}

// proposalSummaryOf returns the API's view of a proposal's summary fields.
func proposalSummaryOf(f proposalFields) apigen.StrategyProposalSummary {
	out := apigen.StrategyProposalSummary{
		Id: f.id, Name: f.name, BaseVersion: f.baseVersion, BaseOrigin: f.baseOrigin, Rationale: f.rationale,
		TriggerCount: int(f.triggerCount), Status: apigen.StrategyProposalStatus(f.status), StatusDetail: f.statusDetail,
		BenchRunId: f.benchRunID, BaselineRunId: f.baselineRunID, Reviewer: f.reviewer,
		CreatedAt: f.createdAt, UpdatedAt: f.updatedAt, DecidedAt: f.decidedAt,
	}

	if f.gateResult != nil {
		out.GateResult = new(apigen.StrategyGateResult(*f.gateResult))
	}

	return out
}

// proposalParams returns the query parameters of a list and its page size.
func proposalParams(p apigen.ListStrategyProposalsParams) (db.ListStrategyProposalsParams, int, error) {
	limit := defaultProposalPage
	if p.Limit != nil {
		limit = *p.Limit
	}

	if limit < 1 || limit > maxProposalPage {
		return db.ListStrategyProposalsParams{}, 0, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxProposalPage)
	}

	params := db.ListStrategyProposalsParams{PageSize: int32(limit + 1), Name: p.Name} //nolint:gosec // limit is at most maxProposalPage.

	if p.Status != nil {
		if !p.Status.Valid() {
			return db.ListStrategyProposalsParams{}, 0, problemf(http.StatusBadRequest, "status is not a proposal status.")
		}

		status := string(*p.Status)
		params.Status = &status
		params.IncludeDrafts = true
	}

	if p.Cursor != nil {
		c, err := parseInvestigationCursor(*p.Cursor)
		if err != nil {
			return db.ListStrategyProposalsParams{}, 0, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		params.CursorAt, params.CursorID = &c.createdAt, &c.id
	}

	return params, limit, nil
}

// proposalActor returns who acts, as the strategy store and the proposal
// service record them.
func proposalActor(ctx context.Context) strategystore.Actor {
	actor := actorOf(ctx)

	return strategystore.Actor{Kind: actor.Kind, ID: actor.ID}
}

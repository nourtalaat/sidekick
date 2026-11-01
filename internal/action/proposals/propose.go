package proposals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// proposeActionDescription is what the model reads about propose_action,
// before the list of actions.
const proposeActionDescription = "Propose one change for a person to approve or reject. Proposing runs nothing: " +
	"the proposal is shown to the responders, and only if an approver approves it before it expires does the " +
	"change happen. Give the action, its parameters exactly as the action's schema asks, and a short reason. " +
	"The parameters are shown to the approver as you write them, so write them in full. " +
	"If the proposal fails its checks you get the errors back and can propose again. The actions this route " +
	"may propose are:"

// proposalNamespace makes the IDs of proposals from the IDs of tool calls.
var proposalNamespace = uuid.MustParse("6f0f6a2e-3c1b-4d3a-9a7e-2b0e0c4f9d11")

// toolInput is the input of propose_action.
type toolInput struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Reason string          `json:"reason"`
}

// ProposeRequest is one propose_action call.
type ProposeRequest struct {
	InvestigationID uuid.UUID
	// ToolCallID is the ID of the model's call. A call repeated after a
	// crash has the same ID, and gets the proposal it made the first time.
	ToolCallID string
	// Route is the investigation's route and Mode its action mode, as it
	// was created.
	Route string
	Mode  action.Mode
	// Input is the call's input.
	Input json.RawMessage
}

// ProposeResult is how a propose_action call ended: the stored proposal,
// or why nothing was stored.
type ProposeResult struct {
	ID        uuid.UUID
	Action    string
	ExpiresAt time.Time
	// Refusal says why nothing was stored, naming the rule, for the model
	// to fix. It is empty when the proposal is stored.
	Refusal string
}

// prepared is a checked proposal, ready to store.
type prepared struct {
	action string
	params json.RawMessage
	reason string
}

// Spec returns the propose_action tool for an investigation of route in
// mode. Its input schema lists only the actions the route may propose, and
// its description gives each one's parameters. It reports false when the
// mode is read-only or the route proposes nothing; a nil Service offers
// the tool nowhere.
func (s *Service) Spec(route string, mode action.Mode) (model.ToolSpec, bool) {
	if s == nil || mode == action.ReadOnly || mode == "" {
		return model.ToolSpec{}, false
	}

	rp, ok := s.opts.Policy.Route(route)
	if !ok || rp.Mode == action.ReadOnly || rp.Mode == "" {
		return model.ToolSpec{}, false
	}

	var (
		names []string
		desc  strings.Builder
	)

	desc.WriteString(proposeActionDescription)

	for _, name := range rp.Actions {
		a, ok := s.opts.Actions[name]
		if !ok {
			continue
		}

		names = append(names, name)
		spec := a.Spec()

		var schema bytes.Buffer
		if err := json.Compact(&schema, spec.ParamsSchema); err != nil {
			schema.Reset()
			schema.WriteString("{}")
		}

		fmt.Fprintf(&desc, "\n- %s: %s Parameters, as a JSON Schema: %s", name, strings.TrimSpace(spec.Description), schema.String())
	}

	if len(names) == 0 {
		return model.ToolSpec{}, false
	}

	schema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"action", "params", "reason"},
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": names},
			"params": map[string]any{"type": "object", "description": "The parameters of the action."},
			"reason": map[string]any{"type": "string", "maxLength": maxReasonChars, "description": "Why this change fixes or prevents the problem, in a few sentences."},
		},
	})
	if err != nil {
		return model.ToolSpec{}, false
	}

	return model.ToolSpec{Name: ProposeActionTool, Description: desc.String(), InputSchema: schema}, true
}

// Propose checks a propose_action call against the route's rules and
// stores the proposal, with its audit entry and the update that shows it.
// A call outside the rules gets a ProposeResult with a Refusal that names
// the rule, and nothing is stored; the error is only for a failure to
// store. A call repeated after a crash returns the proposal it made the
// first time.
func (s *Service) Propose(ctx context.Context, req ProposeRequest) (ProposeResult, error) {
	if s == nil {
		return ProposeResult{Refusal: "propose_action is not available in this deployment"}, nil
	}

	if req.ToolCallID == "" {
		return ProposeResult{}, errNoCallID
	}

	p, refusal := s.prepare(ctx, req)
	if refusal != "" {
		return ProposeResult{Action: p.action, Refusal: refusal}, nil
	}

	var (
		res     ProposeResult
		created bool
	)

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		var err error

		res, created, err = s.store(ctx, tx, req, p)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})
	if err != nil {
		return ProposeResult{}, fmt.Errorf("propose action %s: %w", p.action, err)
	}

	if created {
		s.opts.Metrics.reached(p.action, action.StatusProposed)
		s.log.InfoContext(ctx, "action proposed", "proposal_id", res.ID.String(), "investigation_id", req.InvestigationID.String(), "action", p.action)
	}

	return res, nil
}

// prepare checks the call without the database: the input, the route's
// rules, the parameters' form, schema and limits. It returns the proposal
// to store, or a refusal.
func (s *Service) prepare(ctx context.Context, req ProposeRequest) (prepared, string) {
	if req.Mode == action.ReadOnly || req.Mode == "" {
		return prepared{}, "propose_action is not available: this investigation is read-only"
	}

	var in toolInput

	dec := json.NewDecoder(bytes.NewReader(req.Input))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&in); err != nil {
		return prepared{}, "the input does not decode: " + err.Error()
	}

	p := prepared{action: in.Action}

	if reason := s.notAllowed(req.Route, in.Action); reason != "" {
		return p, "action " + in.Action + " is not allowed: " + reason + "; propose one of the actions in this tool's schema"
	}

	if len(in.Params) == 0 {
		return p, "params is required"
	}

	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return p, "reason is required"
	}

	if len([]rune(reason)) > maxReasonChars {
		return p, fmt.Sprintf("reason is longer than %d characters", maxReasonChars)
	}

	params, err := action.CanonicalParams(in.Params)
	if err != nil {
		return p, "params: " + err.Error()
	}

	if _, err := s.schemas[in.Action].Check(params); err != nil {
		return p, "params: " + cut(err.Error(), maxRefusalChars)
	}

	if err := s.opts.Actions[in.Action].Validate(ctx, params); err != nil {
		return p, fmt.Sprintf("action %s refused these parameters: %s", in.Action, cut(err.Error(), maxRefusalChars))
	}

	p.params, p.reason = params, s.redactReason(reason)

	return p, ""
}

// redactReason redacts the reason when the Service has a redactor.
func (s *Service) redactReason(reason string) string {
	if s.opts.Redactor == nil {
		return reason
	}

	return s.opts.Redactor.Text(reason).Text
}

// store inserts the proposal in tx, unless the call made one already,
// writes its audit entry and refreshes the investigation's outputs. It
// reports whether it created the proposal. The investigation's cap is
// checked first, so a call at the cap that was repeated after a crash still
// finds its proposal.
func (s *Service) store(ctx context.Context, tx pgx.Tx, req ProposeRequest, p prepared) (ProposeResult, bool, error) {
	queries := db.New(tx)
	id := proposalID(req.InvestigationID, req.ToolCallID)

	existing, err := queries.GetActionProposal(ctx, id)
	if err == nil {
		return ProposeResult{ID: id, Action: existing.Action, ExpiresAt: existing.ExpiresAt}, false, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return ProposeResult{}, false, fmt.Errorf("look for proposal %s: %w", id, err)
	}

	count, err := queries.CountInvestigationProposals(ctx, req.InvestigationID)
	if err != nil {
		return ProposeResult{}, false, fmt.Errorf("count the proposals of the investigation: %w", err)
	}

	if count >= int64(s.opts.MaxPerInvestigation) {
		return ProposeResult{Action: p.action, Refusal: fmt.Sprintf("this investigation has made %d proposals, the most it may make", count)}, false, nil
	}

	now := s.now()

	row, err := queries.InsertActionProposal(ctx, db.InsertActionProposalParams{
		ID:              id,
		InvestigationID: req.InvestigationID,
		Action:          p.action,
		Params:          p.params,
		Reason:          p.reason,
		Mode:            string(req.Mode),
		IdempotencyKey:  action.IdempotencyKey(id.String()),
		ProposedAt:      now,
		ExpiresAt:       now.Add(s.opts.TTL),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ProposeResult{ID: id, Action: p.action}, false, nil
	}

	if err != nil {
		return ProposeResult{}, false, fmt.Errorf("insert proposal %s: %w", id, err)
	}

	if err := s.writeAudit(ctx, tx, row, AuditProposed, audit.ActorSidekick, "", map[string]any{
		"status": row.Status, "action": row.Action, "mode": row.Mode, "params": row.Params, "reason": row.Reason, "expiresAt": row.ExpiresAt,
	}); err != nil {
		return ProposeResult{}, false, fmt.Errorf("write the audit entry: %w", err)
	}

	if err := publish.Refresh(ctx, tx, row.InvestigationID); err != nil {
		return ProposeResult{}, false, fmt.Errorf("show proposal %s: %w", id, err)
	}

	return ProposeResult{ID: id, Action: row.Action, ExpiresAt: row.ExpiresAt}, true, nil
}

// proposalID returns the ID of the proposal that a tool call makes. It
// depends only on the call, so repeating the call finds the same row.
func proposalID(investigationID uuid.UUID, toolCallID string) uuid.UUID {
	return uuid.NewSHA1(proposalNamespace, []byte(investigationID.String()+"/"+toolCallID))
}

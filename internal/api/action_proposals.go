package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	actionproposals "github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/problem"
)

// The page sizes of GET /proposals, and the longest note of a rejection,
// in characters.
const (
	defaultProposalsPage = 50
	maxProposalsPage     = 100
	maxRejectNote        = 500
)

// The codes of the 409 answers to a decision, for a program to tell them
// apart.
const (
	codeExpired    = "expired"
	codeDecided    = "decided"
	codeNotAllowed = "not_allowed"

	codeDiffUnavailable = "diff_unavailable"

	codeNotUndoable = "not_undoable"
	codeUndoRunning = "undo_running"
	codeUndoFailed  = "undo_failed"
)

// What a refused decision says.
const (
	detailNoActions = "Action proposals are not set up on this server."
	detailNoProp    = "No action proposal has this ID."
	detailExpired   = "This proposal expired before anyone decided it."
	detailLongNote  = "note must be at most 500 characters."
	detailNoFile    = "This proposal does not change a file with this path."
	detailNoDiff    = "The action that made this proposal cannot read files from its host."

	detailNoActionNames = "No actions are configured, so no action can be filtered."

	detailApproveByPerson = "An action is approved by a person who is signed in, not by an API token."
	detailUndoByPerson    = "An action is undone by a person who is signed in, not by an API token."
	detailUndoRunning     = "An undo of this proposal is running already."
	detailUndoConflict    = "The definition changed after Sidekick changed it. Restoring it overwrites the fields below. Send the request again with force set to restore it anyway."
)

// proposalListed are the statuses GET /proposals lists when none is asked.
var proposalListed = []action.ProposalStatus{action.StatusProposed}

// knownProposalStatuses are the statuses a request may ask for.
var knownProposalStatuses = []action.ProposalStatus{
	action.StatusProposed, action.StatusApproved, action.StatusRejected, action.StatusExpired,
	action.StatusExecuting, action.StatusSucceeded, action.StatusFailed, action.StatusUndone,
}

// ListProposals returns a page of the review queue, newest first.
func (s *server) ListProposals(ctx context.Context, req apigen.ListProposalsRequestObject) (apigen.ListProposalsResponseObject, error) {
	statuses := proposalListed

	if req.Params.Status != nil && len(*req.Params.Status) > 0 {
		statuses = statuses[:0:0]

		for _, st := range *req.Params.Status {
			status := action.ProposalStatus(st)
			if !slices.Contains(knownProposalStatuses, status) {
				return nil, problemf(http.StatusBadRequest, "status must be one of %s.", joinStatuses())
			}

			statuses = append(statuses, status)
		}
	}

	actionName, err := s.proposalActionFilter(req.Params.Action)
	if err != nil {
		return nil, fmt.Errorf("check the action filter: %w", err)
	}

	limit := defaultProposalsPage
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}

	if limit < 1 || limit > maxProposalsPage {
		return nil, problemf(http.StatusBadRequest, "limit must be 1 to %d.", maxProposalsPage)
	}

	var after actionproposals.Cursor

	if req.Params.Cursor != nil {
		c, err := parseProposalCursor(*req.Params.Cursor)
		if err != nil {
			return nil, problemf(http.StatusBadRequest, "cursor is not one this API returned.")
		}

		after = c
	}

	views, err := actionproposals.List(ctx, s.pool, actionproposals.ListOptions{Statuses: statuses, Action: actionName, After: after, PageSize: int32(limit + 1)}) //nolint:gosec // limit is at most maxProposalsPage.
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}

	list := apigen.ListProposals200JSONResponse{Proposals: make([]apigen.Proposal, 0, min(len(views), limit))}

	for i, v := range views {
		if i == limit {
			cursor := proposalCursor(actionproposals.Cursor{ProposedAt: views[i-1].ProposedAt, ID: uuid.MustParse(views[i-1].ID)})
			list.NextCursor = &cursor

			break
		}

		list.Proposals = append(list.Proposals, s.proposalOf(v.Proposal, v.AlertTitle))
	}

	return list, nil
}

// ListProposalActions returns the names of the actions in the config,
// sorted, for the action filter of the review queue.
func (s *server) ListProposalActions(_ context.Context, _ apigen.ListProposalActionsRequestObject) (apigen.ListProposalActionsResponseObject, error) {
	return apigen.ListProposalActions200JSONResponse{Actions: s.actionNames()}, nil
}

// proposalActionFilter returns the action to list: the name asked for, or
// empty for every action. A name that the config does not define is a 400
// that lists the names.
func (s *server) proposalActionFilter(asked *string) (string, error) {
	if asked == nil || *asked == "" {
		return "", nil
	}

	names := s.actionNames()
	if slices.Contains(names, *asked) {
		return *asked, nil
	}

	if len(names) == 0 {
		return "", problemf(http.StatusBadRequest, detailNoActionNames)
	}

	return "", problemf(http.StatusBadRequest, "action must be one of %s.", strings.Join(names, ", "))
}

// actionNames returns the names of the actions in the config, sorted. It
// reads the config in memory, so it is empty when the server has none.
func (s *server) actionNames() []string {
	if s.cfg == nil {
		return []string{}
	}

	names := slices.AppendSeq(make([]string, 0, len(s.cfg.Actions.Definitions)), maps.Keys(s.cfg.Actions.Definitions))
	slices.Sort(names)

	return names
}

// ApproveProposal approves a proposal that waits for a decision.
func (s *server) ApproveProposal(ctx context.Context, req apigen.ApproveProposalRequestObject) (apigen.ApproveProposalResponseObject, error) {
	if s.actions == nil {
		return nil, problemf(http.StatusServiceUnavailable, detailNoActions)
	}

	p, _ := auth.PrincipalFrom(ctx)
	if p.Kind != auth.KindUser {
		return nil, problemf(http.StatusForbidden, detailApproveByPerson)
	}

	got, err := s.actions.Approve(ctx, req.Id, deciderOf(p))
	if err != nil {
		return nil, fmt.Errorf("approve proposal %s: %w", req.Id, decisionProblem(err))
	}

	return apigen.ApproveProposal200JSONResponse(s.proposalOf(got, "")), nil
}

// RejectProposal rejects a proposal that waits for a decision.
func (s *server) RejectProposal(ctx context.Context, req apigen.RejectProposalRequestObject) (apigen.RejectProposalResponseObject, error) {
	if s.actions == nil {
		return nil, problemf(http.StatusServiceUnavailable, detailNoActions)
	}

	var note string
	if req.Body != nil && req.Body.Note != nil {
		note = strings.TrimSpace(*req.Body.Note)
	}

	if len([]rune(note)) > maxRejectNote {
		return nil, problemf(http.StatusBadRequest, detailLongNote)
	}

	p, _ := auth.PrincipalFrom(ctx)

	got, err := s.actions.Reject(ctx, req.Id, deciderOf(p), note)
	if err != nil {
		return nil, fmt.Errorf("reject proposal %s: %w", req.Id, decisionProblem(err))
	}

	return apigen.RejectProposal200JSONResponse(s.proposalOf(got, "")), nil
}

// UndoProposal restores what a succeeded proposal changed.
func (s *server) UndoProposal(ctx context.Context, req apigen.UndoProposalRequestObject) (apigen.UndoProposalResponseObject, error) {
	if s.actions == nil {
		return nil, problemf(http.StatusServiceUnavailable, detailNoActions)
	}

	p, _ := auth.PrincipalFrom(ctx)
	if p.Kind != auth.KindUser {
		return nil, problemf(http.StatusForbidden, detailUndoByPerson)
	}

	force := req.Body != nil && valueOf(req.Body.Force)

	got, err := s.actions.Undo(ctx, req.Id, deciderOf(p), force)
	if err != nil {
		return nil, fmt.Errorf("undo proposal %s: %w", req.Id, undoProblem(err))
	}

	return apigen.UndoProposal200JSONResponse(s.proposalOf(got, "")), nil
}

// GetProposalFileDiff returns the line diff of one file of a pull request
// proposal. The proposal can have any status.
func (s *server) GetProposalFileDiff(ctx context.Context, req apigen.GetProposalFileDiffRequestObject) (apigen.GetProposalFileDiffResponseObject, error) {
	if s.actions == nil {
		return nil, problemf(http.StatusServiceUnavailable, detailNoActions)
	}

	if req.Params.Path == "" {
		return nil, problemf(http.StatusBadRequest, "path is required.")
	}

	diff, err := s.actions.FileDiff(ctx, req.Id, req.Params.Path, valueOf(req.Params.Full))
	if err != nil {
		return nil, fmt.Errorf("diff %q of proposal %s: %w", req.Params.Path, req.Id, s.fileDiffProblem(err))
	}

	out := apigen.GetProposalFileDiff200JSONResponse{
		Path: diff.Path, Status: apigen.ProposalFileDiffStatus(diff.Status), Truncated: diff.Truncated,
		Lines: make([]apigen.ProposalDiffLine, 0, len(diff.Lines)),
	}

	if diff.FinalNewline != "" {
		newline := apigen.ProposalFileDiffFinalNewline(diff.FinalNewline)
		out.FinalNewline = &newline
	}

	for _, l := range diff.Lines {
		out.Lines = append(out.Lines, apigen.ProposalDiffLine{Kind: apigen.ProposalDiffLineKind(l.Kind), OldLine: lineNumber(l.Old), NewLine: lineNumber(l.New), Text: l.Text})
	}

	return out, nil
}

// fileDiffProblem turns the error of a file diff into the answer to give: 404
// for a proposal or a file that does not exist, 409 for an action that cannot
// read files, 502 when the host did not give the base of the file, and the
// error itself otherwise. The message of a failed read is masked, because it
// comes from the host.
func (s *server) fileDiffProblem(err error) error {
	var unreadable *actionproposals.UnreadableError

	switch {
	case errors.Is(err, actionproposals.ErrNotFound):
		return problemf(http.StatusNotFound, detailNoProp)
	case errors.Is(err, actionproposals.ErrNoSuchFile):
		return problemf(http.StatusNotFound, detailNoFile)
	case errors.Is(err, actionproposals.ErrDiffUnavailable):
		return withCode(problemf(http.StatusConflict, detailNoDiff), codeDiffUnavailable)
	case errors.As(err, &unreadable):
		return problemf(http.StatusBadGateway, "Sidekick could not read the file on the base branch from the host: %s", s.mask(unreadable.Err.Error()))
	default:
		return err
	}
}

// lineNumber is the number of a line, or nil for a line that is not in that
// version of the file.
func lineNumber(n int) *int {
	if n == 0 {
		return nil
	}

	return &n
}

// undoProblem turns the error of an undo into the answer to give: 403 for
// a token, 404 for a proposal that does not exist, 409 with a code for one
// that cannot be undone or whose definition changed, and the error itself
// otherwise.
func undoProblem(err error) error {
	var (
		notUndoable *actionproposals.NotUndoableError
		notAllowed  *actionproposals.NotAllowedError
		conflict    *actionproposals.UndoConflictError
		failed      *actionproposals.UndoFailedError
	)

	switch {
	case errors.Is(err, actionproposals.ErrTokenCannotUndo):
		return problemf(http.StatusForbidden, detailUndoByPerson)
	case errors.Is(err, actionproposals.ErrNotFound):
		return problemf(http.StatusNotFound, detailNoProp)
	case errors.Is(err, actionproposals.ErrUndoRunning):
		return withCode(problemf(http.StatusConflict, detailUndoRunning), codeUndoRunning)
	case errors.As(err, &notUndoable):
		return withCode(problemf(http.StatusConflict, "This proposal cannot be undone: %s.", notUndoable.Reason), codeNotUndoable)
	case errors.As(err, &notAllowed):
		return withCode(problemf(http.StatusConflict, "The config does not allow this any more: %s.", notAllowed.Reason), codeNotAllowed)
	case errors.As(err, &conflict):
		e := withCode(problemf(http.StatusConflict, detailUndoConflict), codeConflict)
		e.Changes = problemChanges(conflict.Changes)

		return e
	case errors.As(err, &failed):
		return withCode(problemf(http.StatusConflict, "The undo failed and nothing was restored: %s", failed.Proposal.Error), codeUndoFailed)
	default:
		return err
	}
}

// problemChanges is the difference as the problem details carry it.
func problemChanges(changes []action.Change) []problem.Change {
	out := make([]problem.Change, 0, len(changes))

	for _, c := range changes {
		out = append(out, problem.Change{Path: c.Path, Before: c.Before, After: c.After})
	}

	return out
}

// deciderOf is the person or token that made the request.
func deciderOf(p auth.Principal) actionproposals.Decider {
	return actionproposals.Decider{UserID: p.ID, Token: p.Kind == auth.KindToken}
}

// decisionProblem turns the error of a decision into the answer to give:
// 404 for a proposal that does not exist, 409 with a code for one that
// cannot be decided, and the error itself otherwise.
func decisionProblem(err error) error {
	var (
		decided    *actionproposals.DecidedError
		notAllowed *actionproposals.NotAllowedError
	)

	switch {
	case errors.Is(err, actionproposals.ErrTokenCannotApprove):
		return problemf(http.StatusForbidden, detailApproveByPerson)
	case errors.Is(err, actionproposals.ErrNotFound):
		return problemf(http.StatusNotFound, detailNoProp)
	case errors.Is(err, actionproposals.ErrExpired):
		return withCode(problemf(http.StatusConflict, detailExpired), codeExpired)
	case errors.As(err, &decided):
		return withCode(problemf(http.StatusConflict, "%s", decidedDetail(decided.Proposal)), codeDecided)
	case errors.As(err, &notAllowed):
		return withCode(problemf(http.StatusConflict, "The config does not allow this any more: %s. Reject it instead.", notAllowed.Reason), codeNotAllowed)
	default:
		return err
	}
}

// withCode returns e with its code set.
func withCode(e *Error, code string) *Error {
	e.Code = code

	return e
}

// decidedDetail says what became of a proposal that cannot be decided
// because it was decided, or went further, already, and by whom.
func decidedDetail(p action.Proposal) string {
	by := p.DecidedByName
	if by == "" && p.DecidedBySlackUser != "" {
		by = "the Slack user " + p.DecidedBySlackUser
	}

	if p.ApprovedByPolicy {
		return fmt.Sprintf("This proposal is %s already; it was %s.", p.Status, action.PolicyApprovalText)
	}

	if by == "" {
		return fmt.Sprintf("This proposal is %s already.", p.Status)
	}

	return fmt.Sprintf("This proposal is %s already; %s decided it.", p.Status, by)
}

// proposalOf is the API's view of a proposal. The reason and the error are
// masked; the parameters never are, because they are what runs and what the
// approver reads.
func (s *server) proposalOf(p action.Proposal, alertTitle string) apigen.Proposal {
	out := apigen.Proposal{
		Id:                 uuid.MustParse(p.ID),
		InvestigationId:    uuid.MustParse(p.InvestigationID),
		AlertTitle:         nonEmptyText(alertTitle),
		Route:              p.Route,
		Action:             p.Action,
		Params:             map[string]any{},
		ParamsText:         p.ParamsText,
		Reason:             s.mask(p.Reason),
		Undoable:           p.Undoable,
		Mode:               string(p.Mode),
		Status:             apigen.ProposalStatus(p.Status),
		ProposedAt:         p.ProposedAt,
		ExpiresAt:          p.ExpiresAt,
		DecidedByName:      nonEmptyText(p.DecidedByName),
		DecidedBySlackUser: nonEmptyText(p.DecidedBySlackUser),
		ApprovedByPolicy:   nonFalse(p.ApprovedByPolicy),
		Error:              nonEmptyText(s.mask(p.Error)),
	}

	if err := json.Unmarshal(p.Params, &out.Params); err != nil {
		out.Params = map[string]any{}
	}

	if !p.DecidedAt.IsZero() {
		out.DecidedAt = &p.DecidedAt
	}

	if p.UndoOf != "" {
		undoOf := uuid.MustParse(p.UndoOf)
		out.UndoOf = &undoOf
	}

	if p.Snapshot != nil {
		out.Target = nonEmptyText(p.Snapshot.Target)

		changes := make([]apigen.ProposalChange, 0, len(p.Changes))
		for _, c := range p.Changes {
			changes = append(changes, apigen.ProposalChange{Path: c.Path, Before: c.Before, After: c.After})
		}

		out.Changes = &changes
	}

	if len(p.Files) > 0 {
		diffAvailable := s.actions != nil && s.actions.ReadsFiles(p.Action)
		files := make([]apigen.ProposalFile, 0, len(p.Files))

		for _, f := range p.Files {
			files = append(files, apigen.ProposalFile{
				Path: f.Path, Status: apigen.ProposalFileStatus(f.Status), LinesAdded: f.LinesAdded, LinesRemoved: f.LinesRemoved, DiffAvailable: diffAvailable,
			})
		}

		out.Files = &files
	}

	if p.Result != nil {
		out.Result = &apigen.ActionResult{Summary: s.mask(p.Result.Summary)}

		for _, l := range p.Result.Links {
			links := valueOf(out.Result.Links)
			links = append(links, apigen.ActionLink{Title: s.mask(l.Title), Url: l.URL})
			out.Result.Links = &links
		}
	}

	if p.PullRequest != nil {
		out.PullRequest = pullRequestOf(*p.PullRequest)
	}

	return out
}

// proposalsOf is the API's view of the proposals of an investigation.
func (s *server) proposalsOf(list []action.Proposal) []apigen.Proposal {
	out := make([]apigen.Proposal, 0, len(list))

	for _, p := range list {
		out = append(out, s.proposalOf(p, ""))
	}

	return out
}

// pullRequestOf is the API's view of the state of a proposal's pull request.
func pullRequestOf(pr action.PullRequest) *apigen.ProposalPullRequest {
	return &apigen.ProposalPullRequest{
		Repo:      pr.Repo,
		Number:    pr.Number,
		Url:       pr.URL,
		State:     apigen.ProposalPullRequestState(pr.State),
		MergedAt:  pr.MergedAt,
		ClosedAt:  pr.ClosedAt,
		CheckedAt: pr.CheckedAt,
	}
}

// proposalCursor encodes where a page of proposals ends.
func proposalCursor(c actionproposals.Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.ProposedAt.UnixMicro(), 10) + "." + c.ID.String()))
}

// parseProposalCursor decodes a cursor proposalCursor made.
func parseProposalCursor(s string) (actionproposals.Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return actionproposals.Cursor{}, fmt.Errorf("decode the cursor: %w", err)
	}

	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return actionproposals.Cursor{}, errors.New("the cursor has no ID")
	}

	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return actionproposals.Cursor{}, fmt.Errorf("read the cursor's time: %w", err)
	}

	u, err := uuid.Parse(id)
	if err != nil {
		return actionproposals.Cursor{}, fmt.Errorf("read the cursor's ID: %w", err)
	}

	return actionproposals.Cursor{ProposedAt: time.UnixMicro(n).UTC(), ID: u}, nil
}

// joinStatuses lists the statuses a request may ask for.
func joinStatuses() string {
	names := make([]string, len(knownProposalStatuses))
	for i, st := range knownProposalStatuses {
		names[i] = string(st)
	}

	return strings.Join(names, ", ")
}

// nonFalse returns a pointer to true, or nil for false, so a false flag is
// left out of the answer.
func nonFalse(b bool) *bool {
	if !b {
		return nil
	}

	return &b
}

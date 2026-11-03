package gitchange

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The states of a pull request or merge request, as
// pull_request_states.state holds them.
const (
	StateOpen   = "open"
	StateMerged = "merged"
	StateClosed = "closed"
)

// Limits of one run of the status job.
const (
	// TrackedFor is how long after its proposal succeeded a pull request is
	// still checked. One that stays open longer is given up on.
	TrackedFor = 90 * 24 * time.Hour
	// MaxChecksPerRun is the most pull requests one run reads from the host.
	// The rest wait for the next run.
	MaxChecksPerRun = 1000
)

// Status is what the host says about the pull request of a proposal.
type Status struct {
	Number int
	URL    string
	// State is StateOpen, StateMerged or StateClosed.
	State string
	// MergedAt is set for a merged pull request, ClosedAt for one closed
	// without a merge.
	MergedAt *time.Time
	ClosedAt *time.Time
}

// StateReporter is implemented by an action that opens pull requests or merge
// requests and can say what became of the one a proposal made. The daily job
// calls it for every configured action that implements it.
type StateReporter interface {
	// PullRequestState reads, in any state, the request made from branch
	// (sidekick/<proposal id>). params are the parameters of the proposal that
	// made it; for an undo, those of the proposal it undoes. repo is how the
	// host names the repository or project, and is stored in
	// pull_request_states.repo. A nil Status means there is no such request.
	// An error that wraps action.ErrPermanent means a repeat cannot help (the
	// job logs it and moves on); any other error ends the run so it is tried
	// again.
	PullRequestState(ctx context.Context, params json.RawMessage, branch string) (repo string, st *Status, err error)
	// CountCheck counts one check in the action's own status metric. result is
	// StateOpen, StateMerged or StateClosed for a request that was read and
	// recorded, "missing" for one that was not found, and "error" for a check
	// that failed. The job owns no metric of its own, so each host keeps the
	// counter and the labels it has.
	CountCheck(result string)
}

// RecordState keeps the state of a pull request for the proposal with the ID,
// for the count of merged pull requests. A host calls it when it acts on the
// request itself, for example when it closes the request or opens a revert.
// Like the status job, it changes a row only while the row says open.
func RecordState(ctx context.Context, q db.DBTX, proposalID, repo string, st *Status) error {
	id, err := uuid.Parse(proposalID)
	if err != nil {
		return fmt.Errorf("%w: the proposal ID %q is not an ID: %w", action.ErrPermanent, proposalID, err)
	}

	err = db.New(q).RecordPullRequestState(ctx, db.RecordPullRequestStateParams{
		ProposalID: id, Repo: repo, Number: int64(st.Number), Url: st.URL, State: st.State,
		MergedAt: st.MergedAt, ClosedAt: st.ClosedAt, Now: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("record the state of the pull request of proposal %s: %w", id, err)
	}

	return nil
}

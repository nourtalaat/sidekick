package githubgit

import (
	"context"
	"fmt"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// CloseOriginal is the undo of a pull request that was not merged. It closes
// the pull request orig of repo (owner/name), if it is open, deletes its
// branch and records it as closed for the proposal inv.UndoOf. Closing a
// closed pull request changes nothing, so it is safe to repeat. dbtx may be
// nil, as for Record; m counts the undo as closed.
func CloseOriginal(ctx context.Context, client *gh.Client, dbtx db.DBTX, m *Metrics, inv action.Invocation, repo string, orig *gh.PullRequest, branch string) (action.Result, error) {
	owner, name, _ := strings.Cut(repo, "/")
	title := fmt.Sprintf("%s#%d", repo, orig.GetNumber())
	closed, summary := orig, "The pull request "+title+" was closed already; its branch is deleted."

	if StateOf(orig) == gitchange.StateOpen {
		updated, err := ClosePullRequest(ctx, client, owner, name, orig.GetNumber())
		if err != nil {
			return action.Result{}, fmt.Errorf("close the original: %w", err)
		}

		closed, summary = updated, "Closed the pull request "+title+" and deleted its branch."
	}

	if err := DeleteBranch(ctx, client, owner, name, branch); err != nil {
		return action.Result{}, fmt.Errorf("remove the branch of the closed pull request: %w", err)
	}

	st := StatusOf(closed)
	if st.State == gitchange.StateOpen {
		st.State, st.ClosedAt = gitchange.StateClosed, new(time.Now())
	}

	if err := Record(ctx, dbtx, inv.UndoOf, repo, st); err != nil {
		return action.Result{}, fmt.Errorf("record the original as %s: %w", st.State, err)
	}

	m.PullRequest(ResultClosed)

	return action.Result{Summary: summary, Links: []action.Link{{Title: title, URL: orig.GetHTMLURL()}}}, nil
}

// Record keeps the state of a pull request for the proposal with the ID, for
// the count of merged pull requests. Where there is no database, dbtx is nil
// and it does nothing.
func Record(ctx context.Context, dbtx db.DBTX, proposalID, repo string, st *gitchange.Status) error {
	if dbtx == nil {
		return nil
	}

	if err := gitchange.RecordState(ctx, dbtx, proposalID, repo, st); err != nil {
		return fmt.Errorf("record the pull request state: %w", err)
	}

	return nil
}

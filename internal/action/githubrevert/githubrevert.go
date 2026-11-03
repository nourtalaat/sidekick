// Package githubrevert is the github.revert action: it opens a draft pull
// request that reverts one named commit or one merged pull request, in a
// repository the config allows. It signs in as a GitHub App of its own,
// with write access to contents and pull requests and none to workflows.
//
// GitHub's REST API has no call that reverts a commit, so the action makes
// the revert itself through the Git data API. It compares the tree of the
// commit with the tree of its first parent, and for each file that differs,
// if the head of the base branch still has what the commit wrote, it writes
// what the parent had (or deletes the file the commit added). If any file
// changed since, the whole revert is refused and names the file: there is no
// three-way merge. The result is one commit on the branch
// sidekick/<proposal id> and a pull request, which is always a draft. The
// action never merges, requests reviewers, assigns or turns on auto-merge. A
// repeat after a crash finds the branch and the pull request it made.
//
// What it relies on from GitHub, as the public OpenAPI description
// (github/rest-api-description, commit
// 6348fd455a44a445f9e37a505e981b2c3ed24046, file
// descriptions/api.github.com/api.github.com.2022-11-28.json), the GraphQL
// schema (https://docs.github.com/public/fpt/schema.docs.graphql, published
// from github/docs commit 9e46fa10eb2ed55c59bf6770a220477c91e1b3bf) and
// go-github v93.0.0 (github/git_trees.go, git_commits.go, pulls.go,
// repos_commits.go) say:
//
//   - The commits endpoint cuts a commit's files at 300 and says nothing of
//     the diff of a merge commit, and its entries have no mode or parent
//     blob. The action reads trees instead: git/get-tree, not recursive,
//     gives the mode, type, SHA and size of each entry and says when its
//     list is truncated (more than 100,000 entries).
//   - git/create-tree takes nested paths and a base tree, and a null SHA
//     deletes the entry; it is an error to delete a file that is not there.
//   - pulls/get gives merged, merged_at, commits (the number of commits),
//     base.ref, head.sha, body and merge_commit_sha. For a merge commit it
//     is the merge commit, for a squash the squashed commit, and for a
//     rebase "the commit that the base branch was updated to".
//   - repos/compare-commits gives a status of diverged, ahead, behind or
//     identical, and merge_base_commit. A commit is on a branch when
//     comparing the commit with the branch gives ahead or identical.
//   - pulls/list-files gives filename and previous_filename for each file.
//   - The GraphQL mutation revertPullRequest exists, with a draft input. The
//     schema does not say whether an installation token may call it, and it
//     cannot be proved without a real account, so it is not used.
package githubrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TypeName is the action type this package registers. A config selects it
// with "type: github.revert".
const TypeName = "github.revert"

func init() {
	registry.Actions.Register(TypeName, registry.Typed(New))
}

// Revert is one configured github.revert action. It is safe for concurrent
// use.
type Revert struct {
	name      string
	limits    Limits
	client    *gh.Client
	metrics   *githubgit.Metrics
	log       *slog.Logger
	publicURL string
	// now is the clock the age of a commit is measured by.
	now func() time.Time
	// db is Sidekick's database, where Undo records the state of the pull
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
}

var (
	_ action.Action      = (*Revert)(nil)
	_ action.Snapshotter = (*Revert)(nil)
	_ action.FileReader  = (*Revert)(nil)
)

// New builds the action from its validated config. It makes no network
// call: the first installation token is made by the first call to GitHub.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	return newRevert(name, cfg, deps)
}

// newRevert is New with the concrete type, for the code in this package that
// needs more than the action interface.
func newRevert(name string, cfg Config, deps registry.Deps) (*Revert, error) {
	client, err := githubapp.NewClient(githubapp.Options{
		App:         cfg.App,
		BaseURL:     githubgit.BaseURL(cfg.BaseURL),
		Secrets:     deps.Secrets,
		HTTP:        deps.HTTP,
		UserAgent:   "sidekick/" + buildinfo.Version,
		Permissions: githubapp.ReadWrite(),
	})
	if err != nil {
		return nil, fmt.Errorf("make the GitHub client: %w", err)
	}

	m, err := githubgit.NewMetrics(deps.Metrics, "githubrevert")
	if err != nil {
		return nil, fmt.Errorf("set up the metrics: %w", err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Revert{
		name:      name,
		limits:    cfg.Limits,
		client:    client,
		metrics:   m,
		log:       log,
		publicURL: deps.PublicURL,
		now:       time.Now,
		db:        deps.DB,
	}, nil
}

// Spec implements action.Action. The description tells the model what it
// may revert and when it is refused.
func (r *Revert) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a draft pull request that reverts one commit or one merged pull request, when a recent change caused the alert. ")
	b.WriteString("Name the commit by its full 40-character SHA, or the pull request by its number; never both, and never several. ")
	b.WriteString("It is refused if any file the change touched has changed since, if the change is not on the base branch, or is a revert already made by this action. ")
	b.WriteString("For a pull request that was rebased, or whose files other commits changed since, name its commits one at a time instead. ")
	b.WriteString("To fix forward rather than undo, use github.code_change. ")
	b.WriteString("Allowed repositories: " + strings.Join(r.limits.Repos, ", ") + ". ")
	b.WriteString("Base branch: " + r.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "The change must have landed on the base branch at most %s ago, touch at most %d files and %d added and removed lines in all, and no refused path (CI files, ownership and review rules, Git hooks and settings, secrets and keys, and any path the operator adds).", r.limits.maxAge(), r.limits.MaxFiles, r.limits.MaxChangedLines)

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the repository, the base branch, and that
// exactly one full commit SHA or pull request number is named. What the
// change touches is checked by Snapshot and Execute.
func (r *Revert) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := r.params(raw); err != nil {
		return fmt.Errorf("check the parameters: %w", err)
	}

	return nil
}

// params decodes the parameters and checks them against the config.
func (r *Revert) params(raw json.RawMessage) (Params, error) {
	p, err := decodeParams(raw)
	if err != nil {
		return Params{}, fmt.Errorf("decode the parameters: %w", err)
	}

	if err := p.check(); err != nil {
		return Params{}, fmt.Errorf("check the parameters: %w", err)
	}

	if !githubgit.AllowsRepo(r.limits.Repos, p.Repo) {
		return Params{}, fmt.Errorf("repo %q is not one of limits.repos (%s): %w", p.Repo, strings.Join(r.limits.Repos, ", "), action.ErrLimit)
	}

	if p.BaseBranch != r.limits.BaseBranch {
		return Params{}, fmt.Errorf("baseBranch %q is not the allowed base branch %q: %w", p.BaseBranch, r.limits.BaseBranch, action.ErrLimit)
	}

	return p, nil
}

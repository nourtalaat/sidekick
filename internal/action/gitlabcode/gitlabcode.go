// Package gitlabcode is the gitlab.code_change action: it opens a draft merge
// request that changes application code, in a project the config allows, on
// gitlab.com or a self-managed GitLab. It sends a project, group or personal
// access token with the api scope of its own, and the token's user needs the
// Developer role on the listed projects only. Sidekick never merges the merge
// request, turns on auto-merge or squash, asks for a review or assigns anyone:
// the project's own pipeline and a person decide.
//
// The model gives the whole new content of each file it changes, or deletes
// it. A path must match a glob of limits.paths and none of the refused paths,
// which are a built-in list that an operator can add to but not shorten. No
// validator runs. The merge request is always a draft, from the branch
// sidekick/<proposal id>, with one commit made through the Commits API. A
// repeat after a crash finds the branch and the merge request it made the
// first time, and a merge request that is not a draft is closed and its branch
// deleted, wherever it is found.
//
// The facts about GitLab that the actions share (the Commits API, merge
// requests, quick actions, closing words and file reads) are in package
// gitlabgit. What this action also relies on, as the public source and API
// documentation at the tag v19.5.1-ee of gitlab.com/gitlab-org/gitlab show it
// (paths are under doc/ unless noted):
//
//   - api/repositories.md and lib/gitlab/git/tree.rb: a directory listing gives
//     the id, name, type, path and mode of each entry, and an entry is a tree
//     (a directory), a blob (a file) or a commit (a submodule). The Files API
//     does not say a file's mode, and answers for a symlink with its text, so
//     the action reads the listings of every directory on a path first and
//     only reads a file that is a blob with the mode 100644 or 100755.
//   - api/merge_requests.md and lib/api/entities/merge_request_basic.rb: a
//     merge request has a draft flag, a state of opened, closed, locked or
//     merged, its source and target branch, and its head sha. The draft flag
//     is the title prefix, so the action checks the flag of every merge
//     request it makes or finds.
//   - Commits API actions: an update keeps the file's mode and a create makes
//     a regular file. A file that the change deleted and that was executable
//     is therefore restored as a regular file when the change is undone.
package gitlabcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TypeName is the action type this package registers. A config selects it
// with "type: gitlab.code_change".
const TypeName = "gitlab.code_change"

// Code is one configured gitlab.code_change action. It is safe for concurrent
// use.
type Code struct {
	name      string
	limits    Limits
	client    *gitlabapi.Client
	metrics   *gitlabgit.Metrics
	log       *slog.Logger
	publicURL string
	// db is Sidekick's database, where Undo records the state of the merge
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
}

var (
	_ action.ProposalChecker = (*Code)(nil)
	_ action.Snapshotter     = (*Code)(nil)
)

// newCode builds the action from its validated config. It makes no network
// call: the token is read and used by the first call to GitLab, so GitLab being
// down blocks neither start-up nor readiness.
func newCode(name string, cfg Config, deps registry.Deps) (*Code, error) {
	client, err := gitlabapi.New(gitlabapi.Options{
		BaseURL:   cfg.BaseURL,
		Token:     cfg.TokenSecret,
		Secrets:   deps.Secrets,
		HTTP:      deps.HTTP,
		UserAgent: "sidekick/" + buildinfo.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("make the GitLab client: %w", err)
	}

	m, err := gitlabgit.NewMetrics(deps.Metrics, "gitlabcode")
	if err != nil {
		return nil, fmt.Errorf("set up the metrics: %w", err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Code{
		name:      name,
		limits:    cfg.Limits,
		client:    client,
		metrics:   m,
		log:       log,
		publicURL: deps.PublicURL,
		db:        deps.DB,
	}, nil
}

// Spec implements action.Action. The description tells the model what it may
// change and what happens to its files.
func (c *Code) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a draft merge request that changes application code. ")
	b.WriteString("Use it only for a bug in code, never for a monitor, a threshold or an alert rule. ")
	b.WriteString("Read each file you want to change with the GitLab source first, then give the whole new content of each file you change, or delete it. ")
	b.WriteString("Make the smallest change that fixes the cause of the problem, and leave every other file and line as it is. ")
	b.WriteString("Allowed projects: " + strings.Join(c.limits.Projects, ", ") + ". ")
	b.WriteString("Base branch: " + c.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "At most %d files and %d added and removed lines in all. ", c.limits.MaxFiles, c.limits.MaxChangedLines)
	b.WriteString("Allowed paths: " + strings.Join(c.limits.Paths, ", ") + ". ")
	fmt.Fprintf(&b, "A file may be at most %d KiB. ", gitlabgit.MaxFileBytes>>10)
	b.WriteString("Symlinks, submodules and binary files cannot be changed. ")

	if c.limits.AllowDelete {
		b.WriteString("Files may be deleted. ")
	} else {
		b.WriteString("Deleting files is not allowed. ")
	}

	b.WriteString("These paths are never changed, even when an allowed path matches them: CI and build automation, ownership and review rules, Git settings and hooks, secrets and keys")

	if len(c.limits.RefusedPaths) > 0 {
		b.WriteString(", and " + strings.Join(c.limits.RefusedPaths, ", "))
	}

	b.WriteString(". The merge request is always a draft. ")
	b.WriteString("Sidekick never merges it, asks for a review or assigns anyone: the project's own pipeline runs on it and a person reviews it before it can merge.")

	return action.Spec{Name: c.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the project, the base branch, the number of
// files, their paths, the delete rule and the size of the new content. The
// changed lines, and the files at the base, are checked by CheckProposal and
// Execute.
func (c *Code) Validate(_ context.Context, raw json.RawMessage) error {
	params, err := decodeParams(raw)
	if err != nil {
		return fmt.Errorf("read the parameters: %w", err)
	}

	if _, err := c.limits.plan(params); err != nil {
		return fmt.Errorf("check the limits: %w", err)
	}

	return nil
}

// CheckProposal implements action.ProposalChecker. It reads the files at the
// base branch and counts the changed lines, so the model gets the errors and
// can propose again.
func (c *Code) CheckProposal(ctx context.Context, raw json.RawMessage) error {
	if _, err := c.prepare(ctx, raw, nil); err != nil {
		return fmt.Errorf("check the merge request: %w", err)
	}

	return nil
}

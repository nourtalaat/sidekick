// Package githubcode is the github.code_change action: it opens a draft pull
// request that changes application code, in a repository the config allows.
// It signs in as a GitHub App of its own, with write access to contents and
// pull requests on those repositories only, and without the Workflows
// permission. Sidekick never merges the pull request, turns on auto-merge,
// asks for a review or assigns anyone: the repository's own checks and a
// person decide.
//
// The model gives the whole new content of each file it changes, or deletes
// it. A path must match a glob of limits.paths and none of the refused paths,
// which are a built-in list that an operator can add to but not shorten. No
// validator runs. The pull request is always a draft, from the branch
// sidekick/<proposal id>, with one commit made through the Git data API. A
// repeat after a crash finds the branch and the pull request it made the
// first time.
//
// What the action relies on from GitHub, as the public OpenAPI description
// (github/rest-api-description, commit
// 6348fd455a44a445f9e37a505e981b2c3ed24046, file
// descriptions/api.github.com/api.github.com.2022-11-28.json) and go-github
// v93.0.0 (github/git_trees.go) say:
//
//   - git/get-tree lists one directory, with the path, mode, type, SHA and
//     size of each entry. The modes are 100644 and 100755 for files, 040000
//     for a directory, 160000 for a submodule (type commit) and 120000 for a
//     symlink (a blob). The Contents API has no mode, answers for a symlink
//     with the file it points to and lists a submodule as a file, so the
//     action reads files through the trees API and the blobs API instead.
//   - git/create-tree takes the same modes for its entries. An entry with a
//     null SHA deletes the path, and it is an error to delete a path that
//     does not exist. Content and SHA must not both be set. A file that
//     exists keeps its mode, and a new file is written as 100644.
//   - git/get-blob serves a blob up to 100 megabytes. The limit of 256 KiB
//     for a file is the action's own.
//   - pulls/create takes draft. Its description says drafts exist in public
//     repositories on every plan and in private ones only on some plans, and
//     it does not say what draft does elsewhere, so the action checks the
//     answer and closes a pull request that is not a draft.
//   - An installation token gets only the permissions its request names,
//     and never more than the app has (apps/create-installation-access-token),
//     so Sidekick's token never carries the Workflows permission. The
//     description does not say how GitHub treats a change to a workflow file
//     through the Git data API for an app without it, so the action relies
//     on its refused paths, not on GitHub's answer.
package githubcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// TypeName is the action type this package registers. A config selects it
// with "type: github.code_change".
const TypeName = "github.code_change"

func init() {
	registry.Actions.Register(TypeName, registry.Typed(New))
}

// Code is one configured github.code_change action. It is safe for
// concurrent use.
type Code struct {
	name      string
	limits    Limits
	client    *gh.Client
	metrics   *githubgit.Metrics
	log       *slog.Logger
	publicURL string
	// db is Sidekick's database, where Undo records the state of the pull
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
}

var (
	_ action.Action          = (*Code)(nil)
	_ action.ProposalChecker = (*Code)(nil)
	_ action.Snapshotter     = (*Code)(nil)
	_ action.FileReader      = (*Code)(nil)
	_ action.Undoer          = (*Code)(nil)
)

// New builds the action from its validated config. It makes no network
// call: the first installation token is made by the first call to GitHub, so
// GitHub being down blocks neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	return newCode(name, cfg, deps)
}

// newCode is New with the concrete type, for the code in this package that
// needs more than the action interface.
func newCode(name string, cfg Config, deps registry.Deps) (*Code, error) {
	client, err := githubapp.NewClient(githubapp.Options{
		App:         cfg.App,
		BaseURL:     cfg.baseURL(),
		Secrets:     deps.Secrets,
		HTTP:        deps.HTTP,
		UserAgent:   "sidekick/" + buildinfo.Version,
		Permissions: githubapp.ReadWrite(),
	})
	if err != nil {
		return nil, fmt.Errorf("make the GitHub client: %w", err)
	}

	m, err := githubgit.NewMetrics(deps.Metrics, "githubcode")
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

// Spec implements action.Action. The description tells the model what it
// may change and what happens to its files.
func (c *Code) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a draft pull request that changes application code. ")
	b.WriteString("Use it only for a bug in code, never for a monitor, a threshold or an alert rule. ")
	b.WriteString("Read each file you want to change with the GitHub source first, then give the whole new content of each file you change, or delete it. ")
	b.WriteString("Make the smallest change that fixes the cause of the problem, and leave every other file and line as it is. ")
	b.WriteString("Allowed repositories: " + strings.Join(c.limits.Repos, ", ") + ". ")
	b.WriteString("Base branch: " + c.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "At most %d files and %d added and removed lines in all. ", c.limits.MaxFiles, c.limits.MaxChangedLines)
	b.WriteString("Allowed paths: " + strings.Join(c.limits.Paths, ", ") + ". ")
	fmt.Fprintf(&b, "A file may be at most %d KiB. ", githubgit.MaxFileBytes>>10)
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

	b.WriteString(". The pull request is always a draft. ")
	b.WriteString("Sidekick never merges it, asks for a review or assigns anyone: the repository's own checks run on it and a person reviews it before it can merge.")

	return action.Spec{Name: c.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the repository, the base branch, the number
// of files, their paths, the delete rule and the size of the new content.
// The changed lines, and the files at the base, are checked by
// CheckProposal and Execute.
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
		return fmt.Errorf("check the pull request: %w", err)
	}

	return nil
}

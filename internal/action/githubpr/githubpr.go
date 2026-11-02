// Package githubpr is the github.pull_request action: it opens a pull
// request that changes monitor definitions kept in Git, in a repository the
// config allows. It signs in as a GitHub App of its own, with write access
// to contents and pull requests on the monitors repositories only.
//
// The model gives the whole new content of each file it changes. Every path
// must match a glob in the limits, and the glob gives the file's format.
// Before anything is written, the files go to the monitor validator, and a
// file that fails sends its errors back to the model and opens no pull
// request. The pull request is a draft unless the config says otherwise,
// from the branch sidekick/<proposal id>, with one commit made through the
// Git data API. A repeat after a crash finds the branch and the pull request
// it made the first time.
package githubpr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// TypeName is the action type this package registers. A config selects it
// with "type: github.pull_request".
const TypeName = "github.pull_request"

func init() {
	registry.Actions.Register(TypeName, registry.Typed(New))
}

// PR is one configured github.pull_request action. It is safe for
// concurrent use.
type PR struct {
	name      string
	limits    Limits
	client    *gh.Client
	validator *validateclient.Client
	metrics   *metrics
	log       *slog.Logger
	publicURL string
	// db is Sidekick's database, where Undo records the state of the pull
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
}

var (
	_ action.Action          = (*PR)(nil)
	_ action.ProposalChecker = (*PR)(nil)
	_ action.Snapshotter     = (*PR)(nil)
	_ action.Undoer          = (*PR)(nil)
)

// New builds the action from its validated config. It makes no network
// call: the first installation token is made by the first call to GitHub, so
// GitHub or the validator being down blocks neither start-up nor readiness.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (action.Action, error) {
	return newPR(name, cfg, deps)
}

// newPR is New with the concrete type, for the code in this package that
// needs more than the action interface.
func newPR(name string, cfg Config, deps registry.Deps) (*PR, error) {
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

	m, err := newMetrics(deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up the metrics: %w", err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &PR{
		name:      name,
		limits:    cfg.Limits,
		client:    client,
		validator: validateclient.New(deps.ValidatorURL, deps.HTTP),
		metrics:   m,
		log:       log,
		publicURL: deps.PublicURL,
		db:        deps.DB,
	}, nil
}

// Spec implements action.Action. The description tells the model what it
// may change and what happens to its files.
func (p *PR) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a pull request that changes monitor definitions kept in Git: a threshold, a duration, a filter, an exclusion, or the deletion of a monitor. ")
	b.WriteString("Read the current files first, then give the whole new content of each file you change. ")
	b.WriteString("Allowed repositories: " + strings.Join(p.limits.Repos, ", ") + ". ")
	b.WriteString("Base branch: " + p.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "At most %d files and %d added and removed lines in all. ", p.limits.MaxFiles, p.limits.MaxChangedLines)
	b.WriteString("Allowed paths and their formats: ")

	for i, rule := range p.limits.Paths {
		if i > 0 {
			b.WriteString(", ")
		}

		fmt.Fprintf(&b, "%s (%s)", rule.Glob, rule.Format)
	}

	b.WriteString(". Every file is checked with the validator for its format; if it finds errors you get them back and no pull request is opened.")

	return action.Spec{Name: p.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the repository, the base branch, the number
// of files and their paths. The changed lines, and the files themselves,
// are checked by CheckProposal and Execute.
func (p *PR) Validate(_ context.Context, raw json.RawMessage) error {
	params, err := decodeParams(raw)
	if err != nil {
		return fmt.Errorf("read the parameters: %w", err)
	}

	if _, err := p.limits.plan(params); err != nil {
		return fmt.Errorf("check the limits: %w", err)
	}

	return nil
}

// CheckProposal implements action.ProposalChecker. It reads the files at
// the base branch, counts the changed lines and validates the files, so
// the model gets the errors and can propose again.
func (p *PR) CheckProposal(ctx context.Context, raw json.RawMessage) error {
	if _, err := p.prepare(ctx, raw, nil); err != nil {
		return fmt.Errorf("check the pull request: %w", err)
	}

	return nil
}

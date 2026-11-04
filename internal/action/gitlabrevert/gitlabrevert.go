package gitlabrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Revert is one configured gitlab.revert action. It is safe for concurrent
// use.
type Revert struct {
	name      string
	limits    Limits
	client    *gitlabapi.Client
	metrics   *gitlabgit.Metrics
	log       *slog.Logger
	publicURL string
	// now is the clock the age of a commit is measured by.
	now func() time.Time
	// db is Sidekick's database, where Undo records the state of the merge
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
	// CIReading reads the pipelines of the merge requests the action opens.
	*gitlabgit.CIReading
}

// newRevert builds the action from its validated config. It makes no network
// call: the token is read and used by the first call to GitLab.
func newRevert(name string, cfg Config, deps registry.Deps) (*Revert, error) {
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

	m, err := gitlabgit.NewMetrics(deps.Metrics, "gitlabrevert")
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
		CIReading: gitlabgit.NewCIReading(client, cfg.Timeout()),
	}, nil
}

// Spec implements action.Action. The description tells the model what it
// may revert and when it is refused.
func (r *Revert) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a draft merge request that reverts one commit or one merged merge request, when a recent change caused the alert. ")
	b.WriteString("Name the commit by its full 40-character SHA, or the merge request by its IID; never both, and never several. ")
	b.WriteString("It is refused if any file the change touched has changed since, if the change is not on the base branch, or is a revert already made by this action. ")
	b.WriteString("A merge request merged by fast-forward with several commits is refused: name its commits one at a time instead. ")
	b.WriteString("To fix forward rather than undo, use gitlab.merge_request. ")
	b.WriteString("Allowed projects: " + strings.Join(r.limits.Projects, ", ") + ". ")
	b.WriteString("Base branch: " + r.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "The change must have landed on the base branch at most %s ago, touch at most %d files and %d added and removed lines in all, and no refused path (CI files, ownership and review rules, Git hooks and settings, secrets and keys, and any path the operator adds).", r.limits.maxAge(), r.limits.MaxFiles, r.limits.MaxChangedLines)

	return action.Spec{Name: r.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the project, the base branch, and that exactly
// one full commit SHA or merge request IID is named. What the change touches is
// checked by Snapshot and Execute.
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

	if !gitlabgit.AllowsProject(r.limits.Projects, p.Project) {
		return Params{}, fmt.Errorf("project %q is not one of limits.projects (%s): %w", p.Project, strings.Join(r.limits.Projects, ", "), action.ErrLimit)
	}

	if p.BaseBranch != r.limits.BaseBranch {
		return Params{}, fmt.Errorf("baseBranch %q is not the allowed base branch %q: %w", p.BaseBranch, r.limits.BaseBranch, action.ErrLimit)
	}

	return p, nil
}

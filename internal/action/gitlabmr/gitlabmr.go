// Package gitlabmr is the gitlab.merge_request action: it opens a merge
// request that changes monitor definitions kept in Git, in a project the
// config allows, on gitlab.com or a self-managed GitLab. It sends a project,
// group or personal access token with the api scope, and the token's user
// needs the Developer role on the monitors projects only.
//
// The model gives the whole new content of each file it changes. Every path
// must match a glob in the limits, and the glob gives the file's format.
// Before anything is written, the files go to the monitor validator, and a
// file that fails sends its errors back to the model and makes no branch. The
// merge request is a draft unless the config says otherwise, from the branch
// sidekick/<proposal id>, with one commit made through the Commits API. A
// repeat after a crash finds the merge request, or the branch, that the first
// attempt made. The action never assigns, asks for a review, approves, sets
// auto-merge or squash, or merges.
//
// What the action relies on from GitLab, as the public source and API
// documentation at the tag v19.5.1-ee of gitlab.com/gitlab-org/gitlab show
// them (paths are under doc/ unless noted, and Gitaly is at the tag v19.5.1
// of gitlab.com/gitlab-org/gitaly):
//
//   - api/commits.md and lib/api/commits.rb: POST /projects/:id/repository/commits
//     takes a branch, a commit_message, a start_sha and actions of create,
//     update, delete, move and chmod, each with a file_path and, except for a
//     delete, a content that is text unless the encoding says base64. A branch
//     that does not exist is created when a start_sha (or start_branch) is
//     given, so the branch and its commit appear together. When the branch
//     exists already and the start differs, the answer is 400 "A branch called
//     ... already exists" unless force is set, which the action never does
//     (app/services/commits/create_service.rb). Every failure of the call is a
//     400 with a message.
//   - Gitaly internal/gitaly/service/operations/commit_files.go and
//     lib/gitlab/gitaly_client/operation_service.rb: a create of a path that
//     exists answers "A file with this name already exists", an update or
//     delete of a path that is not a file answers "A file with this name
//     doesn't exist", an update keeps the file's mode, and a create over a
//     directory replaces it, so the action never creates a file whose name is
//     a directory. The action picks create, update or delete from what it
//     read at the start_sha, so none of these happens unless the files moved.
//   - api/merge_requests.md and lib/api/merge_requests.rb: POST
//     /projects/:id/merge_requests takes a source_branch, a target_branch, a
//     title and a description, among other fields the action never sends, and
//     answers 409 when another open merge request has the same source branch
//     (lib/api/helpers/merge_requests_helpers.rb, app/models/merge_request.rb).
//     It has no parameter for a draft: a merge request is a draft when its title
//     starts with "Draft:", "[Draft]" or "(Draft)" in any case
//     (app/models/merge_request.rb DRAFT_REGEX, lib/gitlab/regex/merge_requests.rb).
//     PUT /projects/:id/merge_requests/:iid takes a state_event of close or
//     reopen. A merge request is opened, closed, locked (being merged) or
//     merged. GET /projects/:id/merge_requests takes source_branch and state=all
//     to find a merge request in any state.
//   - user/project/quick_actions.md, app/services/issuable_base_service.rb and
//     lib/gitlab/quick_actions/extractor.rb: a description is read for quick
//     actions when it is saved, with the rights of the token's user. A command
//     is a slash and a name at the start of a line, matched in any case, after
//     the extractor deletes every carriage return; text in a code span is
//     ignored. text.go puts such a line in a code span.
//   - config/initializers/1_settings.rb: the words that close an issue when a
//     merge request merges (close, fix, resolve and implement, in their forms)
//     and what may follow them. text.go puts the target in a code span.
//   - api/repository_files.md, api/repositories.md and lib/api/files.rb: a file
//     is read at a commit with GET .../repository/files/:path?ref=<sha>, which
//     answers its blob_id, its size and its base64 content, or 404 for a path
//     that is no file; a directory is listed with GET .../repository/tree, which
//     has no sizes, and a blob is read by its ID with GET
//     .../repository/blobs/:sha. Text that is not stored as UTF-8 is converted
//     before it is encoded, so the action refuses a file whose decoded length is
//     not its size. A branch is read with GET .../repository/branches/:branch,
//     which answers its commit, and removed with DELETE on the same path.
//   - security/tokens/access_token_scopes.md, user/project/repository/branches/protected.md
//     and auth/tokens/fine_grained_access_tokens.md: the api scope is the only
//     scope of a project or group token that allows these writes. A protected
//     branch with "Allowed to push and merge" set to No one and "Allowed to
//     merge" set to Maintainers cannot be changed or merged by a Developer, so
//     it is the hard limit.
package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// TypeName is the action type this package registers. A config selects it
// with "type: gitlab.merge_request".
const TypeName = "gitlab.merge_request"

// MR is one configured gitlab.merge_request action. It is safe for
// concurrent use.
type MR struct {
	name      string
	limits    Limits
	client    *gitlabapi.Client
	validator validateclient.Validator
	metrics   *metrics
	log       *slog.Logger
	publicURL string
	// db is Sidekick's database, where Undo records the state of the merge
	// requests it closes or reverts. It is nil where plugins are built
	// without one, and nothing is recorded then.
	db db.DBTX
}

var (
	_ action.ProposalChecker = (*MR)(nil)
	_ action.Snapshotter     = (*MR)(nil)
	_ action.FileReader      = (*MR)(nil)
	_ action.MonitorFixer    = (*MR)(nil)
)

// newMR is New with the concrete type, for the code in this package that
// needs more than the action interface.
func newMR(name string, cfg Config, deps registry.Deps) (*MR, error) {
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

	m, err := newMetrics(deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up the metrics: %w", err)
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	validator := deps.Validator
	if validator == nil {
		validator = validateclient.New(deps.ValidatorURL, deps.HTTP)
	}

	return &MR{
		name:      name,
		limits:    cfg.Limits,
		client:    client,
		validator: validator,
		metrics:   m,
		log:       log,
		publicURL: deps.PublicURL,
		db:        deps.DB,
	}, nil
}

// Spec implements action.Action. The description tells the model what it
// may change and what happens to its files.
func (m *MR) Spec() action.Spec {
	var b strings.Builder

	b.WriteString("Opens a merge request that changes monitor definitions kept in Git: a threshold, a duration, a filter, an exclusion, or the deletion of a monitor. ")
	b.WriteString("Read the current files first, then give the whole new content of each file you change. ")
	b.WriteString("Allowed projects: " + strings.Join(m.limits.Projects, ", ") + ". ")
	b.WriteString("Base branch: " + m.limits.BaseBranch + ". ")
	fmt.Fprintf(&b, "At most %d files and %d added and removed lines in all. ", m.limits.MaxFiles, m.limits.MaxChangedLines)
	b.WriteString("Allowed paths and their formats: ")

	for i, rule := range m.limits.Paths {
		if i > 0 {
			b.WriteString(", ")
		}

		fmt.Fprintf(&b, "%s (%s)", rule.Glob, rule.Format)
	}

	b.WriteString(". Every file is checked with the validator for its format; if it finds errors you get them back and no merge request is opened.")

	return action.Spec{Name: m.name, Description: b.String(), ParamsSchema: paramsSchema}
}

// FixKind implements action.MonitorFixer.
func (*MR) FixKind() action.FixKind {
	return action.FixPullRequest
}

// Validate implements action.Action. It checks the parameters against the
// limits that need no network: the project, the base branch, the number of
// files and their paths. The changed lines, and the files themselves, are
// checked by CheckProposal and Execute.
func (m *MR) Validate(_ context.Context, raw json.RawMessage) error {
	params, err := decodeParams(raw)
	if err != nil {
		return fmt.Errorf("read the parameters: %w", err)
	}

	if _, err := m.limits.plan(params); err != nil {
		return fmt.Errorf("check the limits: %w", err)
	}

	return nil
}

// CheckProposal implements action.ProposalChecker. It reads the files at
// the base branch, counts the changed lines and validates the files, so
// the model gets the errors and can propose again.
func (m *MR) CheckProposal(ctx context.Context, raw json.RawMessage) error {
	if _, err := m.prepare(ctx, raw, nil); err != nil {
		return fmt.Errorf("check the merge request: %w", err)
	}

	return nil
}

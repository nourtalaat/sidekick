package gitlabrevert

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// commitPattern is the shape of a full Git commit SHA. A branch, a tag or a
// short SHA names a different commit as time goes by, so none is accepted.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var errCommitOrMergeRequest = errors.New("give either commit or mergeRequest, not both and not neither")

// Params are what the model fills in when it proposes the action: the
// project, the one thing to revert, and the reason.
type Params struct {
	// Project is the project, as its full path.
	Project string `json:"project"`
	// BaseBranch is the branch the revert merge request targets.
	BaseBranch string `json:"baseBranch"`
	// Commit is the full SHA of the commit to revert. A merge commit is
	// reverted against its first parent.
	Commit string `json:"commit,omitempty"`
	// MergeRequest is the IID of the merged merge request to revert.
	MergeRequest int `json:"mergeRequest,omitempty"`
	// Title and Body are the merge request's: why the change is reverted.
	Title string `json:"title"`
	Body  string `json:"body"`
}

// paramsSchema is the JSON Schema of Params. It does not say that exactly
// one of commit and mergeRequest is needed, because some model providers
// refuse a schema with oneOf at its top; Params.check does.
var paramsSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["project", "baseBranch", "title", "body"],
  "properties": {
    "project": {"type": "string", "description": "The project, as its full path, such as group/project."},
    "baseBranch": {"type": "string", "description": "The branch the revert merge request targets."},
    "commit": {"type": "string", "pattern": "^[0-9a-f]{40}$", "description": "The full 40-character SHA of the one commit to revert. Give this or mergeRequest."},
    "mergeRequest": {"type": "integer", "minimum": 1, "description": "The IID of the one merged merge request to revert. Give this or commit."},
    "title": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The merge request title: what is reverted and why, in one line."},
    "body": {"type": "string", "maxLength": 8000, "description": "The merge request description: the reason for the revert."}
  }
}`, gitchange.MaxTitleChars))

// decodeParams decodes the parameters, refusing unknown fields.
func decodeParams(raw json.RawMessage) (Params, error) {
	p, err := gitchange.DecodeParams[Params](raw)
	if err != nil {
		return Params{}, fmt.Errorf("read the gitlab.revert parameters: %w", err)
	}

	return p, nil
}

// check checks the parts of the parameters that need neither the config nor
// the network: that the project is a full path, that exactly one of commit and
// mergeRequest is given, and that the commit is a full SHA.
func (p Params) check() error {
	switch {
	case !gitlabapi.ValidProject(p.Project):
		return fmt.Errorf("project %q must be a full project path such as group/project", p.Project)
	case (p.Commit == "") == (p.MergeRequest == 0):
		return errCommitOrMergeRequest
	case p.Commit != "" && !commitPattern.MatchString(p.Commit):
		return fmt.Errorf("commit %q must be a full 40-character lower-case SHA, not a branch, a tag or a short SHA", p.Commit)
	case p.MergeRequest < 0:
		return fmt.Errorf("mergeRequest %d must be a merge request IID", p.MergeRequest)
	}

	if err := gitchange.CheckText(p.Title, p.Body); err != nil {
		return fmt.Errorf("check the title and body: %w", err)
	}

	return nil
}

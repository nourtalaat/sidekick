package githubrevert

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// commitPattern is the shape of a full Git commit SHA. A branch, a tag or a
// short SHA names a different commit as time goes by, so none is accepted.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var errCommitOrPullRequest = errors.New("give either commit or pullRequest, not both and not neither")

// Params are what the model fills in when it proposes the action: the
// repository, the one thing to revert, and the reason.
type Params struct {
	// Repo is the repository, as owner/name.
	Repo string `json:"repo"`
	// BaseBranch is the branch the revert pull request targets.
	BaseBranch string `json:"baseBranch"`
	// Commit is the full SHA of the commit to revert. A merge commit is
	// reverted against its first parent.
	Commit string `json:"commit,omitempty"`
	// PullRequest is the number of the merged pull request to revert.
	PullRequest int `json:"pullRequest,omitempty"`
	// Title and Body are the pull request's: why the change is reverted.
	Title string `json:"title"`
	Body  string `json:"body"`
}

// paramsSchema is the JSON Schema of Params. It does not say that exactly
// one of commit and pullRequest is needed, because some model providers
// refuse a schema with oneOf at its top; Params.check does.
var paramsSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["repo", "baseBranch", "title", "body"],
  "properties": {
    "repo": {"type": "string", "description": "The repository, as owner/name."},
    "baseBranch": {"type": "string", "description": "The branch the revert pull request targets."},
    "commit": {"type": "string", "pattern": "^[0-9a-f]{40}$", "description": "The full 40-character SHA of the one commit to revert. Give this or pullRequest."},
    "pullRequest": {"type": "integer", "minimum": 1, "description": "The number of the one merged pull request to revert. Give this or commit."},
    "title": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The pull request title: what is reverted and why, in one line."},
    "body": {"type": "string", "maxLength": 8000, "description": "The pull request description: the reason for the revert."}
  }
}`, gitchange.MaxTitleChars))

// decodeParams decodes the parameters, refusing unknown fields.
func decodeParams(raw json.RawMessage) (Params, error) {
	p, err := gitchange.DecodeParams[Params](raw)
	if err != nil {
		return Params{}, fmt.Errorf("read the github.revert parameters: %w", err)
	}

	return p, nil
}

// check checks the parts of the parameters that need neither the config nor
// the network: that exactly one of commit and pullRequest is given, and
// that the commit is a full SHA.
func (p Params) check() error {
	switch {
	case (p.Commit == "") == (p.PullRequest == 0):
		return errCommitOrPullRequest
	case p.Commit != "" && !commitPattern.MatchString(p.Commit):
		return fmt.Errorf("commit %q must be a full 40-character lower-case SHA, not a branch, a tag or a short SHA", p.Commit)
	case p.PullRequest < 0:
		return fmt.Errorf("pullRequest %d must be a pull request number", p.PullRequest)
	}

	if err := gitchange.CheckText(p.Title, p.Body); err != nil {
		return fmt.Errorf("check the title and body: %w", err)
	}

	return nil
}

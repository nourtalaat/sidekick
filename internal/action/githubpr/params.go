package githubpr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// Params are what the model fills in when it proposes the action: the
// repository, and the branch, text and files of the pull request.
type Params struct {
	// Repo is the repository, as owner/name.
	Repo string `json:"repo"`

	gitchange.Edit
}

// paramsSchema is the JSON Schema of Params.
var paramsSchema = gitchange.ParamsSchema("repo", "The repository, as owner/name.")

// decodeParams decodes the parameters, refusing unknown fields.
func decodeParams(raw json.RawMessage) (Params, error) {
	p, err := gitchange.DecodeParams[Params](raw)
	if err != nil {
		return Params{}, fmt.Errorf("read the github.pull_request parameters: %w", err)
	}

	return p, nil
}

// plan checks the parameters against the limits that need no network and
// returns the changes with the format of each. A limit it breaks wraps
// action.ErrLimit.
func (l *Limits) plan(p Params) ([]gitchange.PlannedFile, error) {
	if !l.allowsRepo(p.Repo) {
		return nil, fmt.Errorf("repo %q is not one of limits.repos (%s): %w", p.Repo, strings.Join(l.Repos, ", "), action.ErrLimit)
	}

	changes, err := gitchange.Plan(p.Edit, l.Bounds, l.Paths.FormatOf)
	if err != nil {
		return nil, fmt.Errorf("check the parameters: %w", err)
	}

	return changes, nil
}

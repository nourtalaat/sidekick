package gitlabmr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// Params are what the model fills in when it proposes the action: the
// project, and the branch, text and files of the merge request.
type Params struct {
	// Project is the project, as its full path.
	Project string `json:"project"`

	gitchange.Edit
}

// paramsSchema is the JSON Schema of Params.
var paramsSchema = gitchange.ParamsSchema("project", "The project, as its full path, such as group/project.")

// decodeParams decodes the parameters, refusing unknown fields.
func decodeParams(raw json.RawMessage) (Params, error) {
	p, err := gitchange.DecodeParams[Params](raw)
	if err != nil {
		return Params{}, fmt.Errorf("read the gitlab.merge_request parameters: %w", err)
	}

	return p, nil
}

// plan checks the parameters against the limits that need no network and
// returns the changes with the format of each. A limit it breaks wraps
// action.ErrLimit.
func (l *Limits) plan(p Params) ([]gitchange.PlannedFile, error) {
	if !l.allowsProject(p.Project) {
		return nil, fmt.Errorf("project %q is not one of limits.projects (%s): %w", p.Project, strings.Join(l.Projects, ", "), action.ErrLimit)
	}

	changes, err := gitchange.Plan(p.Edit, l.Bounds, l.Paths.FormatOf)
	if err != nil {
		return nil, fmt.Errorf("check the parameters: %w", err)
	}

	return changes, nil
}

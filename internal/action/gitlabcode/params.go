package gitlabcode

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/validateclient"
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
		return Params{}, fmt.Errorf("read the gitlab.code_change parameters: %w", err)
	}

	return p, nil
}

// plan checks the parameters against the limits that need no network and
// returns the changes. A limit it breaks wraps action.ErrLimit. A path that
// matches a refused path is refused even when a glob of limits.paths allows
// it.
func (l *Limits) plan(p Params) ([]gitchange.PlannedFile, error) {
	if !l.allowsProject(p.Project) {
		return nil, fmt.Errorf("project %q is not one of limits.projects (%s): %w", p.Project, strings.Join(l.Projects, ", "), action.ErrLimit)
	}

	changes, err := gitchange.Plan(p.Edit, l.Bounds, anyPath)
	if err != nil {
		return nil, fmt.Errorf("check the parameters: %w", err)
	}

	for _, c := range changes {
		if err := l.checkFile(c); err != nil {
			return nil, fmt.Errorf("check the files: %w", err)
		}
	}

	return changes, nil
}

// allowsProject reports whether project is one of the configured projects.
func (l *Limits) allowsProject(project string) bool {
	return gitlabgit.AllowsProject(l.Projects, project)
}

// checkFile checks one change against the paths, the delete rule and the size
// limit.
func (l *Limits) checkFile(c gitchange.PlannedFile) error {
	if err := gitchange.CheckCodePath(c.Path, l.Paths, l.RefusedPaths); err != nil {
		return fmt.Errorf("check the path: %w", err)
	}

	if c.Deleted() && !l.AllowDelete {
		return fmt.Errorf("path %q cannot be deleted: limits.allowDelete is false: %w", c.Path, action.ErrLimit)
	}

	if !c.Deleted() && len(*c.Content) > gitlabgit.MaxFileBytes {
		return fmt.Errorf("the new content of %q is larger than %d bytes: %w", c.Path, gitlabgit.MaxFileBytes, action.ErrLimit)
	}

	return nil
}

// anyPath is the path rule given to gitchange.Plan: a code change has no
// format, and its paths are checked by checkFile, which names the path once.
func anyPath(string) (validateclient.Format, error) {
	return "", nil
}

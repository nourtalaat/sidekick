package gitlabgit

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// CheckProjects checks that there is at least one project and that each is a
// full path. Each error starts with the key limits.projects.
func CheckProjects(projects []string) []error {
	if len(projects) == 0 {
		return []error{errors.New("limits.projects: list at least one project, as its full path such as acme/platform/monitors")}
	}

	var errs []error

	for i, project := range projects {
		if !gitlabapi.ValidProject(project) {
			errs = append(errs, fmt.Errorf("limits.projects[%d]: %q must be a full project path such as group/project, with no wildcards", i, project))
		}
	}

	return errs
}

// AllowsProject reports whether project is one of the projects. GitLab paths
// are not case-sensitive, so neither is the match.
func AllowsProject(projects []string, project string) bool {
	return slices.ContainsFunc(projects, func(p string) bool { return strings.EqualFold(p, project) })
}

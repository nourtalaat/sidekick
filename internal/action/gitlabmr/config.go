package gitlabmr

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Config is the section of one gitlab.merge_request action.
type Config struct {
	// BaseURL is the root of the GitLab, for example
	// https://gitlab.example.com. Empty means gitlab.com.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the access token: a project, group or personal
	// access token with the api scope and the Developer role on the
	// monitors projects only. It is read on every call.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Limits say what the action may touch.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a gitlab.merge_request action.
type Limits struct {
	// Projects are the projects the action may change, as full paths such as
	// acme/platform/monitors.
	Projects []string `yaml:"projects"`
	// Paths say which files may change and the format each is checked as.
	// The first rule that matches a path decides.
	Paths gitchange.PathRules `yaml:"paths"`
	// Bounds are maxFiles, maxChangedLines and baseBranch.
	gitchange.Bounds `yaml:",inline"`
	// Draft opens the merge requests as drafts. Nil means true.
	Draft *bool `yaml:"draft"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.TokenSecret.Name == "" || c.TokenSecret.Key == "" {
		errs = append(errs, errors.New("tokenSecret: required, as { name, key }"))
	}

	if err := gitlabapi.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns an error for each limit that is missing or out of range.
func (l *Limits) check() []error {
	var errs []error

	errs = append(errs, l.checkProjects()...)
	errs = append(errs, l.Paths.Check()...)
	errs = append(errs, l.Check()...)

	return errs
}

// checkProjects checks that there is at least one project and that each is
// a full path.
func (l *Limits) checkProjects() []error {
	if len(l.Projects) == 0 {
		return []error{errors.New("limits.projects: list at least one project, as its full path such as acme/platform/monitors")}
	}

	var errs []error

	for i, project := range l.Projects {
		if !gitlabapi.ValidProject(project) {
			errs = append(errs, fmt.Errorf("limits.projects[%d]: %q must be a full project path such as group/project, with no wildcards", i, project))
		}
	}

	return errs
}

// draft reports whether merge requests open as drafts.
func (l *Limits) draft() bool {
	return l.Draft == nil || *l.Draft
}

// allowsProject reports whether project is one of the configured projects.
// GitLab paths are not case-sensitive, so neither is the match.
func (l *Limits) allowsProject(project string) bool {
	return slices.ContainsFunc(l.Projects, func(p string) bool { return strings.EqualFold(p, project) })
}

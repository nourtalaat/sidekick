package gitlabcode

import (
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Config is the section of one gitlab.code_change action. There is no key for
// a draft: the merge request is always a draft, so a config that sets draft is
// an unknown key error.
type Config struct {
	// BaseURL is the root of the GitLab, for example
	// https://gitlab.example.com. Empty means gitlab.com.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the access token: a project, group or personal
	// access token with the api scope and the Developer role on the listed
	// projects only. It is a token of its own, apart from the one of the
	// GitLab source and of the monitor merge request action. It is read on
	// every call.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// CIConfig is ciReportTimeout, how long Sidekick waits for the pipeline
	// of a merge request to finish.
	gitchange.CIConfig `yaml:",inline"`
	// Limits say what the action may touch.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a gitlab.code_change action.
type Limits struct {
	// Projects are the projects the action may change, as full paths such as
	// acme/platform/api.
	Projects []string `yaml:"projects"`
	// Paths are globs of the files that may change. A segment of ** matches
	// any number of segments.
	Paths []string `yaml:"paths"`
	// RefusedPaths are globs of files that never change. They are added to
	// the built-in refused paths, which an operator cannot remove, and win
	// over Paths.
	RefusedPaths []string `yaml:"refusedPaths"`
	// Bounds are maxFiles, maxChangedLines and baseBranch.
	gitchange.Bounds `yaml:",inline"`
	// AllowDelete lets a change delete files. The default is false.
	AllowDelete bool `yaml:"allowDelete"`
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

	errs = append(errs, c.Check()...)
	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns an error for each limit that is missing or out of range.
func (l *Limits) check() []error {
	var errs []error

	errs = append(errs, gitlabgit.CheckProjects(l.Projects)...)
	errs = append(errs, l.checkPaths()...)
	errs = append(errs, gitchange.CheckRefusedGlobs(l.RefusedPaths)...)
	errs = append(errs, l.Check()...)

	return errs
}

// checkPaths checks that there is at least one glob and that each is valid.
func (l *Limits) checkPaths() []error {
	if len(l.Paths) == 0 {
		return []error{errors.New(`limits.paths: list at least one glob, such as "src/**"`)}
	}

	var errs []error

	for i, glob := range l.Paths {
		if err := gitchange.CheckGlob(glob); err != nil {
			errs = append(errs, fmt.Errorf("limits.paths[%d]: %w", i, err))
		}
	}

	return errs
}

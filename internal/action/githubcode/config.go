package githubcode

import (
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/githubapp"
)

// Config is the section of one github.code_change action. There is no key
// for a draft: the pull request is always a draft, so a config that sets
// draft is an unknown key error.
type Config struct {
	// App is the GitHub App the action authenticates as. It is an app of
	// its own, apart from the one the GitHub source uses and the one of the
	// monitor pull request action, installed only on limits.repos, with
	// Contents (read and write), Pull requests (read and write) and Metadata
	// (read), and without Workflows.
	App githubapp.App `yaml:"app"`
	// BaseURL is the REST API root, for GitHub Enterprise Server, for
	// example https://github.example.com/api/v3/. Empty means github.com.
	BaseURL string `yaml:"baseURL"`
	// CIConfig is ciReportTimeout, how long Sidekick waits for the CI of a
	// pull request to finish. The app also needs Checks (read) and Commit
	// statuses (read) for that; without them the CI result is unknown.
	gitchange.CIConfig `yaml:",inline"`
	// Limits say what the action may touch.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a github.code_change action.
type Limits struct {
	// Repos are the repositories the action may change, as owner/name.
	Repos []string `yaml:"repos"`
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

	errs = append(errs, c.App.Check("app")...)

	if err := githubgit.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Check()...)
	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// baseURL returns the REST API root, ending in a slash.
func (c *Config) baseURL() string {
	return githubgit.BaseURL(c.BaseURL)
}

// check returns an error for each limit that is missing or out of range.
func (l *Limits) check() []error {
	var errs []error

	errs = append(errs, githubgit.CheckRepos(l.Repos)...)
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

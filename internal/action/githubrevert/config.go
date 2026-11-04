package githubrevert

import (
	"errors"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/githubapp"
)

// The longest a commit may have been on the base branch before the action
// refuses to revert it, and the most a config may allow.
const (
	defaultMaxAge = 72 * time.Hour
	ceilingMaxAge = 30 * 24 * time.Hour
)

// Config is the section of one github.revert action.
type Config struct {
	// App is the GitHub App the action authenticates as. It is an app of
	// its own, with write access to contents and pull requests and no
	// access to workflows.
	App githubapp.App `yaml:"app"`
	// BaseURL is the REST API root, for GitHub Enterprise Server, for
	// example https://github.example.com/api/v3/. Empty means github.com.
	BaseURL string `yaml:"baseURL"`
	// CIConfig is ciReportTimeout, how long Sidekick waits for the CI of a
	// pull request to finish. The app also needs Checks (read) and Commit
	// statuses (read) for that; without them the CI result is unknown.
	gitchange.CIConfig `yaml:",inline"`
	// Limits say what the action may revert.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a github.revert action. There is no draft key:
// the pull request is always a draft.
type Limits struct {
	// Repos are the repositories the action may change, as owner/name.
	Repos []string `yaml:"repos"`
	// Bounds are maxFiles, maxChangedLines and baseBranch.
	gitchange.Bounds `yaml:",inline"`
	// MaxAge is how long ago a commit may have landed on the base branch.
	// Empty means 72 hours; the most is 30 days.
	MaxAge time.Duration `yaml:"maxAge"`
	// RefusedPaths are globs added to the built-in refused paths: a revert
	// of a commit that touches one is refused.
	RefusedPaths []string `yaml:"refusedPaths"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, c.App.Check("app")...)

	if err := githubgit.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("check the base URL: %w", err))
	}

	errs = append(errs, c.Check()...)
	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns an error for each limit that is missing or out of range.
func (l *Limits) check() []error {
	errs := githubgit.CheckRepos(l.Repos)
	errs = append(errs, l.Check()...)
	errs = append(errs, gitchange.CheckRefusedGlobs(l.RefusedPaths)...)

	if l.MaxAge < 0 || l.MaxAge > ceilingMaxAge {
		errs = append(errs, fmt.Errorf("limits.maxAge: must be from 1ns to %s, or empty for %s, got %s", ceilingMaxAge, defaultMaxAge, l.MaxAge))
	}

	return errs
}

// maxAge returns the longest age, with the default filled in.
func (l *Limits) maxAge() time.Duration {
	if l.MaxAge == 0 {
		return defaultMaxAge
	}

	return l.MaxAge
}

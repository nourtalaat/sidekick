package githubpr

import (
	"errors"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/githubapp"
)

// Config is the section of one github.pull_request action.
type Config struct {
	// App is the GitHub App the action authenticates as. It is an app of
	// its own, apart from the one the GitHub source uses, with write access
	// to contents and pull requests on the monitors repositories only.
	App githubapp.App `yaml:"app"`
	// BaseURL is the REST API root, for GitHub Enterprise Server, for
	// example https://github.example.com/api/v3/. Empty means github.com.
	BaseURL string `yaml:"baseURL"`
	// Limits say what the action may touch.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a github.pull_request action.
type Limits struct {
	// Repos are the repositories the action may change, as owner/name.
	Repos []string `yaml:"repos"`
	// Paths say which files may change and the format each is checked as.
	// The first rule that matches a path decides.
	Paths gitchange.PathRules `yaml:"paths"`
	// Bounds are maxFiles, maxChangedLines and baseBranch.
	gitchange.Bounds `yaml:",inline"`
	// Draft opens the pull requests as drafts. Nil means true.
	Draft *bool `yaml:"draft"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, c.App.Check("app")...)

	if err := githubgit.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

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
	errs = append(errs, l.Paths.Check()...)
	errs = append(errs, l.Check()...)

	return errs
}

// draft reports whether pull requests open as drafts.
func (l *Limits) draft() bool {
	return l.Draft == nil || *l.Draft
}

package gitlabrevert

import (
	"errors"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The longest a commit may have been on the base branch before the action
// refuses to revert it, and the most a config may allow.
const (
	defaultMaxAge = 72 * time.Hour
	ceilingMaxAge = 30 * 24 * time.Hour
)

// Config is the section of one gitlab.revert action.
type Config struct {
	// BaseURL is the root of the GitLab, for example
	// https://gitlab.example.com. Empty means gitlab.com.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the access token: a project, group or personal
	// access token with the api scope and the Developer role on the projects
	// in limits.projects only. It is read on every call.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Limits say what the action may revert.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a gitlab.revert action. There is no draft key: the
// merge request is always a draft.
type Limits struct {
	// Projects are the projects the action may change, as full paths such as
	// acme/platform/deploy.
	Projects []string `yaml:"projects"`
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
	errs := gitlabgit.CheckProjects(l.Projects)
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

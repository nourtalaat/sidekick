package githubpr

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// defaultBaseURL is the REST API root of github.com.
const defaultBaseURL = "https://api.github.com/"

// repoPattern is the shape of a repository name, owner/name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

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

	if err := c.checkBaseURL(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// checkBaseURL checks that the base URL, when set, is an absolute https
// URL, so the installation token never crosses the network unencrypted.
// Plain http is allowed only for a loopback host.
func (c *Config) checkBaseURL() error {
	if c.BaseURL == "" {
		return nil
	}

	u, err := url.Parse(c.BaseURL)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("baseURL: %q must be an absolute https URL", c.BaseURL)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", c.BaseURL)
	default:
		return nil
	}
}

// baseURL returns the REST API root, ending in a slash.
func (c *Config) baseURL() string {
	switch {
	case c.BaseURL == "":
		return defaultBaseURL
	case strings.HasSuffix(c.BaseURL, "/"):
		return c.BaseURL
	default:
		return c.BaseURL + "/"
	}
}

// check returns an error for each limit that is missing or out of range.
func (l *Limits) check() []error {
	var errs []error

	errs = append(errs, l.checkRepos()...)
	errs = append(errs, l.Paths.Check()...)
	errs = append(errs, l.Bounds.Check()...)

	return errs
}

// checkRepos checks that there is at least one repository and that each is
// a plain owner/name.
func (l *Limits) checkRepos() []error {
	if len(l.Repos) == 0 {
		return []error{errors.New("limits.repos: list at least one repository, as owner/name")}
	}

	var errs []error

	for i, repo := range l.Repos {
		if !validRepo(repo) {
			errs = append(errs, fmt.Errorf("limits.repos[%d]: %q must have the form owner/name, with no wildcards", i, repo))
		}
	}

	return errs
}

// draft reports whether pull requests open as drafts.
func (l *Limits) draft() bool {
	return l.Draft == nil || *l.Draft
}

// allowsRepo reports whether repo is one of the configured repositories.
// GitHub names are not case-sensitive, so neither is the match.
func (l *Limits) allowsRepo(repo string) bool {
	return slices.ContainsFunc(l.Repos, func(r string) bool { return strings.EqualFold(r, repo) })
}

// validRepo reports whether s is a repository name, owner/name. Neither
// part may be "." or "..", which GitHub does not allow and which would
// change the meaning of an API path.
func validRepo(s string) bool {
	owner, name, _ := strings.Cut(s, "/")

	return repoPattern.MatchString(s) && !dots(owner) && !dots(name)
}

// dots reports whether s is "." or "..".
func dots(s string) bool {
	return s == "." || s == ".."
}

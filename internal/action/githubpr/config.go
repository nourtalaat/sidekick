package githubpr

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// defaultBaseURL is the REST API root of github.com.
const defaultBaseURL = "https://api.github.com/"

// Ceilings for the limits an operator sets, so a config cannot ask for a
// pull request that no person would review.
const (
	maxFilesCeiling        = 100
	maxChangedLinesCeiling = 1000
)

// repoPattern is the shape of a repository name, owner/name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// branchPattern is the shape of a branch name this action accepts: letters,
// digits and . _ - /, where no segment is empty or starts with a dot or a
// hyphen.
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// knownFormats are the formats a path rule may name.
var knownFormats = []validateclient.Format{
	validateclient.FormatPrometheusRules,
	validateclient.FormatTerraform,
	validateclient.FormatGrafanaAlertFile,
}

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
	Paths []PathRule `yaml:"paths"`
	// MaxFiles is the most files one pull request may change.
	MaxFiles int `yaml:"maxFiles"`
	// MaxChangedLines is the most lines one pull request may add or remove
	// in all files together.
	MaxChangedLines int `yaml:"maxChangedLines"`
	// BaseBranch is the only branch a pull request may target.
	BaseBranch string `yaml:"baseBranch"`
	// Draft opens the pull requests as drafts. Nil means true.
	Draft *bool `yaml:"draft"`
}

// PathRule says that files matching Glob are checked as Format.
type PathRule struct {
	// Glob matches a path in the repository: segments are matched with
	// path.Match, and a segment of ** matches any number of segments.
	Glob string `yaml:"glob"`
	// Format is prometheus_rules, terraform or grafana_alert_file.
	Format validateclient.Format `yaml:"format"`
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
	errs = append(errs, l.checkPaths()...)

	if l.MaxFiles < 1 || l.MaxFiles > maxFilesCeiling {
		errs = append(errs, fmt.Errorf("limits.maxFiles: must be from 1 to %d, got %d", maxFilesCeiling, l.MaxFiles))
	}

	if l.MaxChangedLines < 1 || l.MaxChangedLines > maxChangedLinesCeiling {
		errs = append(errs, fmt.Errorf("limits.maxChangedLines: must be from 1 to %d, got %d", maxChangedLinesCeiling, l.MaxChangedLines))
	}

	switch {
	case l.BaseBranch == "":
		errs = append(errs, errors.New("limits.baseBranch: required"))
	case !branchPattern.MatchString(l.BaseBranch) || strings.Contains(l.BaseBranch, ".."):
		errs = append(errs, fmt.Errorf("limits.baseBranch: %q is not a branch name this action accepts", l.BaseBranch))
	}

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

// checkPaths checks that there is at least one rule and that each has a
// valid glob and a known format.
func (l *Limits) checkPaths() []error {
	if len(l.Paths) == 0 {
		return []error{errors.New("limits.paths: list at least one rule, such as {glob: \"prometheus/rules/**\", format: prometheus_rules}")}
	}

	var errs []error

	for i, rule := range l.Paths {
		key := fmt.Sprintf("limits.paths[%d]", i)

		if err := checkGlob(rule.Glob); err != nil {
			errs = append(errs, fmt.Errorf("%s.glob: %w", key, err))
		}

		switch {
		case rule.Format == "":
			errs = append(errs, fmt.Errorf("%s.format: required; use one of %s", key, formatNames()))
		case !slices.Contains(knownFormats, rule.Format):
			errs = append(errs, fmt.Errorf("%s.format: unknown format %q; use one of %s", key, rule.Format, formatNames()))
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

// formatFor returns the format of the first rule that matches path.
func (l *Limits) formatFor(path string) (validateclient.Format, bool) {
	for _, rule := range l.Paths {
		if matchGlob(rule.Glob, path) {
			return rule.Format, true
		}
	}

	return "", false
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

// formatNames lists the known formats, for error messages.
func formatNames() string {
	names := make([]string, len(knownFormats))
	for i, f := range knownFormats {
		names[i] = string(f)
	}

	return strings.Join(names, ", ")
}

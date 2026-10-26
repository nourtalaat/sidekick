package github

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultBaseURL is the REST API root of github.com.
const defaultBaseURL = "https://api.github.com/"

// repoPattern is the shape of a repository name, owner/name.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// repoGlobPattern is the shape of a repos entry: owner/name, where either
// part may hold path.Match wildcards.
var repoGlobPattern = regexp.MustCompile(`^[A-Za-z0-9_.*?\[\]^!-]+/[A-Za-z0-9_.*?\[\]^!-]+$`)

// Config is the config section of one GitHub source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the REST API root, for GitHub Enterprise Server, for
	// example https://github.example.com/api/v3/. Empty means github.com.
	BaseURL string `yaml:"baseURL"`
	// App is the GitHub App Sidekick authenticates as.
	App App `yaml:"app"`
	// Repos are the repositories the tools may read, as owner/name globs
	// such as acme/*. A call for any other repository fails.
	Repos []string `yaml:"repos"`
	// ServiceRepos maps a service name to its repository, so a tool can be
	// called with the service an alert names.
	ServiceRepos map[string]string `yaml:"serviceRepos"`
}

// App identifies one installation of a GitHub App.
type App struct {
	// AppID is the app's numeric ID, from its settings page.
	AppID int64 `yaml:"appID"`
	// InstallationID is the ID of the app's installation on the account
	// that owns the repositories.
	InstallationID int64 `yaml:"installationID"`
	// PrivateKeySecret holds the app's private key, in PEM form.
	PrivateKeySecret registry.SecretRef `yaml:"privateKeySecret"`
}

// Validate checks the section without secrets or network calls: the app
// is fully named, the base URL is an absolute https URL, every
// repos entry is an owner/name glob, every serviceRepos repository is one
// that repos allows, and the timeout fits the HTTP client. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.App.AppID <= 0 {
		errs = append(errs, errors.New("app.appID: required"))
	}

	if c.App.InstallationID <= 0 {
		errs = append(errs, errors.New("app.installationID: required"))
	}

	if c.App.PrivateKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("app.privateKeySecret: required"))
	}

	if err := c.checkBaseURL(); err != nil {
		errs = append(errs, err)
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	errs = append(errs, c.checkRepos()...)
	errs = append(errs, c.checkServiceRepos()...)

	return errors.Join(errs...)
}

// checkBaseURL checks that the base URL, when set, is an absolute https
// URL, so the installation token never crosses the network unencrypted.
// Plain http is allowed only for a loopback host, such as a local test
// server.
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

// checkRepos checks that repos has at least one entry and that each is a
// valid owner/name glob.
func (c *Config) checkRepos() []error {
	if len(c.Repos) == 0 {
		return []error{errors.New("repos: list at least one repository or owner/name glob, such as acme/*")}
	}

	var errs []error

	for i, glob := range c.Repos {
		key := fmt.Sprintf("repos[%d]", i)

		if !repoGlobPattern.MatchString(glob) {
			errs = append(errs, fmt.Errorf("%s: %q must have the form owner/name, with optional wildcards", key, glob))

			continue
		}

		if _, err := path.Match(glob, ""); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid glob %q: %w", key, glob, err))
		}
	}

	return errs
}

// checkServiceRepos checks that every service maps to an owner/name that
// repos allows.
func (c *Config) checkServiceRepos() []error {
	var errs []error

	services := make([]string, 0, len(c.ServiceRepos))
	for service := range c.ServiceRepos {
		services = append(services, service)
	}

	slices.Sort(services)

	for _, service := range services {
		repo := c.ServiceRepos[service]
		key := "serviceRepos." + service

		switch {
		case strings.TrimSpace(service) == "":
			errs = append(errs, errors.New("serviceRepos: a service name must not be empty"))
		case !validRepo(repo):
			errs = append(errs, fmt.Errorf("%s: %q must have the form owner/name", key, repo))
		case !c.allows(repo):
			errs = append(errs, fmt.Errorf("%s: %q is not in repos", key, repo))
		}
	}

	return errs
}

// allows reports whether repos allows the repository owner/name. GitHub
// names are not case-sensitive, so neither is the match.
func (c *Config) allows(repo string) bool {
	repo = strings.ToLower(repo)

	for _, glob := range c.Repos {
		if ok, err := path.Match(strings.ToLower(glob), repo); err == nil && ok {
			return true
		}
	}

	return false
}

// baseURL returns the configured REST API root, ending in a slash, or
// defaultBaseURL.
func (c *Config) baseURL() string {
	if c.BaseURL == "" {
		return defaultBaseURL
	}

	if strings.HasSuffix(c.BaseURL, "/") {
		return c.BaseURL
	}

	return c.BaseURL + "/"
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

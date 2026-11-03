package gitlab

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultBaseURL is the root of gitlab.com.
const defaultBaseURL = "https://gitlab.com"

// apiSuffix is the path of the REST API under the root of a GitLab.
const apiSuffix = "/api/v4"

// projectPattern is the shape of a project's full path, group/project or
// group/subgroup/project: two or more parts of the characters GitLab allows
// in a path.
var projectPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)

// Config is the config section of one GitLab source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the root of the GitLab, for example https://gitlab.example.com
	// or, for a GitLab served under a sub-path, https://example.com/gitlab.
	// Empty means gitlab.com.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the access token Sidekick sends: a project, group or
	// personal access token with the read_api scope and the Reporter role.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Projects are the projects the tools may read, as full paths that may
	// hold globs, such as acme/*. A call for any other project fails.
	Projects []string `yaml:"projects"`
	// ServiceProjects maps a service name to its project, so a tool can be
	// called with the service an alert names.
	ServiceProjects map[string]string `yaml:"serviceProjects"`
}

// Validate checks the section without secrets or network calls: the token
// secret is named, the base URL is an absolute https URL for the root of a
// GitLab, every projects entry is a project path glob, every
// serviceProjects project is one that projects allows, and the timeout fits
// the HTTP client. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.TokenSecret.Name == "" || c.TokenSecret.Key == "" {
		errs = append(errs, errors.New("tokenSecret: required, as { name, key }"))
	}

	if err := c.checkBaseURL(); err != nil {
		errs = append(errs, err)
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	errs = append(errs, c.checkProjects()...)
	errs = append(errs, c.checkServiceProjects()...)

	return errors.Join(errs...)
}

// checkBaseURL checks that the base URL, when set, is an absolute https URL
// for the root of a GitLab, so the token never crosses the network
// unencrypted. Plain http is allowed only for a loopback host, such as a
// local test server.
func (c *Config) checkBaseURL() error {
	if c.BaseURL == "" {
		return nil
	}

	u, err := url.Parse(c.BaseURL)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://gitlab.example.com", c.BaseURL)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", c.BaseURL)
	case strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), apiSuffix):
		return fmt.Errorf("baseURL: %q must be the root of the GitLab, without %s", c.BaseURL, apiSuffix)
	default:
		return nil
	}
}

// checkProjects checks that projects has at least one entry and that each
// is a valid project path glob.
func (c *Config) checkProjects() []error {
	if len(c.Projects) == 0 {
		return []error{errors.New("projects: list at least one project path or glob, such as acme/*")}
	}

	var errs []error

	for i, glob := range c.Projects {
		if err := checkProjectGlob(glob); err != nil {
			errs = append(errs, fmt.Errorf("projects[%d]: %w", i, err))
		}
	}

	return errs
}

// checkServiceProjects checks that every service maps to a project path
// that projects allows.
func (c *Config) checkServiceProjects() []error {
	var errs []error

	services := make([]string, 0, len(c.ServiceProjects))
	for service := range c.ServiceProjects {
		services = append(services, service)
	}

	slices.Sort(services)

	for _, service := range services {
		project := c.ServiceProjects[service]
		key := "serviceProjects." + service

		switch {
		case strings.TrimSpace(service) == "":
			errs = append(errs, errors.New("serviceProjects: a service name must not be empty"))
		case !validProject(project):
			errs = append(errs, fmt.Errorf("%s: %q must be a full project path such as group/project", key, project))
		case !c.allows(project):
			errs = append(errs, fmt.Errorf("%s: %q is not in projects", key, project))
		}
	}

	return errs
}

// allows reports whether projects allows the project path. GitLab paths are
// not case-sensitive, so neither is the match.
func (c *Config) allows(project string) bool {
	project = strings.ToLower(project)

	for _, glob := range c.Projects {
		if matchGlob(strings.ToLower(glob), project) {
			return true
		}
	}

	return false
}

// baseURL returns the root of the GitLab, without a trailing slash, or
// defaultBaseURL.
func (c *Config) baseURL() string {
	if c.BaseURL == "" {
		return defaultBaseURL
	}

	return strings.TrimSuffix(c.BaseURL, "/")
}

// validProject reports whether s is a project's full path. No part may be
// "." or "..", which would change the meaning of an API path.
func validProject(s string) bool {
	if !projectPattern.MatchString(s) {
		return false
	}

	for part := range strings.SplitSeq(s, "/") {
		if dots(part) {
			return false
		}
	}

	return true
}

// dots reports whether s is "." or "..".
func dots(s string) bool {
	return s == "." || s == ".."
}

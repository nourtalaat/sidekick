package gitlab

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

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

	if err := gitlabapi.CheckBaseURL(c.BaseURL); err != nil {
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
		case !gitlabapi.ValidProject(project):
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
// gitlab.com.
func (c *Config) baseURL() string {
	return gitlabapi.BaseURL(c.BaseURL)
}

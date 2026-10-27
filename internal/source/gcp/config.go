package gcp

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// projectPattern is the shape of a Google Cloud project ID, with the
// optional domain prefix of older projects, such as example.com:shop.
var projectPattern = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*[a-z0-9]:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// Config is the config section of one Google Cloud source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Projects are the IDs of the projects the tools may read. A call for
	// any other project is a tool error, and nothing is sent.
	Projects []string `yaml:"projects"`
	// CredentialsSecret holds a credential file: a workload identity
	// federation (external_account) file, or a service account key. Empty
	// means Application Default Credentials, which on GKE come from
	// Workload Identity.
	CredentialsSecret *registry.SecretRef `yaml:"credentialsSecret"`
}

// Validate checks the section without secrets or network calls: projects
// lists at least one valid project ID, each once, and the timeout fits the
// HTTP client. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Projects) == 0 {
		errs = append(errs, errors.New("projects: list at least one project ID"))
	}

	seen := map[string]bool{}

	for i, p := range c.Projects {
		key := fmt.Sprintf("projects[%d]", i)

		switch {
		case !projectPattern.MatchString(p):
			errs = append(errs, fmt.Errorf("%s: %q is not a project ID, such as shop-prod", key, p))
		case seen[p]:
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", key, p))
		}

		seen[p] = true
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

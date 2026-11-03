package argocd

import (
	"errors"
	"fmt"
	"regexp"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// namespacePattern is the shape of a Kubernetes namespace name.
var namespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Config is the config section of one Argo CD source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the root of the Argo CD server, with its sub-path when it
	// is served from one. It must be https, or http on a loopback host.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the API token Sidekick sends. The token of an
	// account that may only get applications is enough.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Applications are the applications the tools may read, as
	// project/name globs, which is how Argo CD RBAC names them. A call for
	// any other application is a tool error, and nothing is sent.
	Applications argo.Allowlist `yaml:"applications"`
	// Namespace is the namespace Argo CD runs in, and so the namespace of
	// the applications the tools read. Empty means argocd.
	Namespace string `yaml:"namespace"`
}

// Validate checks the section without secrets or network calls: the token
// secret is named, the base URL is an absolute https URL, applications
// lists valid globs, the namespace, when set, is a namespace name, and the
// timeout fits the HTTP client. Each error starts with the key of its
// field.
func (c *Config) Validate() error {
	var errs []error

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if c.BaseURL == "" {
		errs = append(errs, errors.New("baseURL: required, the address of your Argo CD server"))
	} else if err := argo.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if err := c.Applications.Check("applications"); err != nil {
		errs = append(errs, err)
	}

	if c.Namespace != "" && !namespacePattern.MatchString(c.Namespace) {
		errs = append(errs, fmt.Errorf("namespace: %q is not a Kubernetes namespace name", c.Namespace))
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

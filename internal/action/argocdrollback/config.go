package argocdrollback

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// namespacePattern is the shape of a Kubernetes namespace name.
var namespacePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Config is the config section of one argocd.rollback action. Embedding the
// gate makes the action approval-only, and embedding the revision limit adds
// the key maxRevisionsBack.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`

	// BaseURL is the root of the Argo CD server, with its sub-path when it
	// is served from one. It must be https, or http on a loopback host.
	// Required.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the API token Sidekick sends. Give the action a token
	// of its own, with the Argo CD permissions get and sync on the allowed
	// applications and nothing else, apart from the token of any Argo CD
	// source. Required.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Namespace is the namespace Argo CD runs in, and so the namespace of the
	// applications the action rolls back. Empty means argocd.
	Namespace string `yaml:"namespace"`
	// Limits say which applications the action may roll back.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an argocd.rollback action. They are checked when
// the model proposes, again right before the rollback is requested, and when
// it is undone.
type Limits struct {
	// Applications are the applications the action may roll back, as
	// project/name globs, which is how Argo CD RBAC names them. Required, so
	// nothing is allowed by default.
	Applications argocd.Allowlist `yaml:"applications"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.BaseURL == "" {
		errs = append(errs, errors.New("baseURL: required, the address of your Argo CD server"))
	} else if err := argocd.CheckBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if c.Namespace != "" && !namespacePattern.MatchString(c.Namespace) {
		errs = append(errs, fmt.Errorf("namespace: %q is not a Kubernetes namespace name", c.Namespace))
	}

	if err := c.Limits.Applications.Check("limits.applications"); err != nil {
		errs = append(errs, err)
	}

	if err := c.RevisionLimit.CheckConfig(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

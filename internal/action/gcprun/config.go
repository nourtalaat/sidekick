package gcprun

import (
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// maxServiceLen is the longest name of a Cloud Run service: Google asks for
// fewer than 50 characters.
const maxServiceLen = 49

var (
	// projectPattern is the shape of a Google Cloud project ID, with the
	// optional domain prefix of older projects, such as example.com:shop.
	projectPattern = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*[a-z0-9]:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	// locationPattern is the shape of a Google Cloud region, such as
	// europe-west1.
	locationPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)+$`)
	// servicePattern is the shape of a Cloud Run service name: it starts
	// with a letter, holds lowercase letters, digits and hyphens, does not
	// end with a hyphen and has fewer than 50 characters.
	servicePattern = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,47}[a-z0-9])?$`)
	// servicePatternGlob is servicePattern with "*" allowed anywhere, for the
	// entries of limits.services.
	servicePatternGlob = regexp.MustCompile(`^[a-z*]([a-z0-9*-]{0,47}[a-z0-9*])?$`)
)

// Config is the config section of one gcp.cloud_run_rollback action.
// Embedding the gate makes the action approval-only, and the revision limit
// adds maxRevisionsBack.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`

	// CredentialsSecret holds a credential file: a workload identity
	// federation (external_account) file, or a service account key. Empty
	// means Application Default Credentials, which on GKE come from Workload
	// Identity. Give the action a credential of its own, apart from the one
	// of any Google Cloud source.
	CredentialsSecret *registry.SecretRef `yaml:"credentialsSecret"`
	// Limits say which services the action may roll back.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a gcp.cloud_run_rollback action. They are checked
// when the model proposes, again right before the change is written, and when
// it is undone. Everything is required, so nothing is allowed by default.
type Limits struct {
	// Projects are the IDs of the projects whose services may be rolled back.
	Projects []string `yaml:"projects"`
	// Locations are the regions of the services.
	Locations []string `yaml:"locations"`
	// Services are service names, or patterns in which "*" stands for any
	// text, such as checkout-*.
	Services []string `yaml:"services"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := c.CheckConfig(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	errs = append(errs, checkList("limits.projects", "project ID, such as shop-prod", l.Projects, projectPattern)...)
	errs = append(errs, checkList("limits.locations", "region, such as europe-west1", l.Locations, locationPattern)...)
	errs = append(errs, checkList("limits.services", "service name, or a pattern with *", l.Services, servicePatternGlob)...)

	return errs
}

// checkList checks that a list has at least one entry, that every entry
// matches pattern, and that none is listed twice.
func checkList(key, what string, list []string, pattern *regexp.Regexp) []error {
	if len(list) == 0 {
		return []error{fmt.Errorf("%s: list at least one", key)}
	}

	var errs []error

	for i, entry := range list {
		switch {
		case !pattern.MatchString(entry):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not a %s", key, i, entry, what))
		case slices.Index(list, entry) != i:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, entry))
		}
	}

	return errs
}

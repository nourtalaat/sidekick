package awsecs

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

// namePattern is the shape of an ECS cluster or service name. ARNs are not
// taken, since one can name another account.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)

// servicePattern is the shape of an entry of limits.services: a service
// name in which "*" may stand for any run of characters. "*" is the only
// wildcard: the pattern holds no other character that a glob reads.
var servicePattern = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9_*-]{0,254}$`)

// Config is the config section of one aws.ecs_rollback action. Embedding the
// gate makes the action approval-only.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`
	awsaction.Config     `yaml:",inline"`

	// Limits say which services the action may roll back.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an aws.ecs_rollback action. They are checked when
// the model proposes, again right before the change is written, and when it
// is undone.
type Limits struct {
	// Clusters are the names of the clusters the action may change.
	// Required.
	Clusters []string `yaml:"clusters"`
	// Services are service names, or patterns in which "*" stands for any
	// run of characters, such as checkout-*. A service in any allowed
	// cluster with such a name may be rolled back. Required.
	Services []string `yaml:"services"`
}

// Validate checks the section without credentials or network calls. Each
// error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := c.Config.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := c.CheckConfig(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, checkList("limits.clusters", "cluster name", c.Limits.Clusters, namePattern)...)
	errs = append(errs, checkList("limits.services", "service name or pattern", c.Limits.Services, servicePattern)...)

	return errors.Join(errs...)
}

// checkList checks that a list has at least one entry, that every entry
// matches pattern, and that none is listed twice.
func checkList(at, what string, list []string, pattern *regexp.Regexp) []error {
	if len(list) == 0 {
		return []error{fmt.Errorf("%s: list at least one", at)}
	}

	var errs []error

	seen := map[string]bool{}

	for i, entry := range list {
		switch {
		case !pattern.MatchString(entry):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not a %s", at, i, entry, what))
		case seen[entry]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", at, i, entry))
		}

		seen[entry] = true
	}

	return errs
}

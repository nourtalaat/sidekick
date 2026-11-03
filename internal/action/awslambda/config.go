package awslambda

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/awsaction"
)

// functionPattern is the shape of an entry of limits.functions: a function
// name in which "*" may stand for any run of characters. "*" is the only
// wildcard: the pattern holds no other character that a glob reads.
var functionPattern = regexp.MustCompile(`^[A-Za-z0-9_*-]{1,64}$`)

// aliasPattern is the shape of an alias name.
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// allDigits matches a name that Lambda would read as a version number, which
// is not an alias.
var allDigits = regexp.MustCompile(`^[0-9]+$`)

// Config is the config section of one aws.lambda_rollback action. Embedding
// the gate makes the action approval-only.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`
	awsaction.Config     `yaml:",inline"`

	// Limits say which aliases the action may point at another version.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an aws.lambda_rollback action. They are checked
// when the model proposes, again right before the change is written, and when
// it is undone.
type Limits struct {
	// Functions are function names, or patterns in which "*" stands for any
	// run of characters, such as checkout-*. Required.
	Functions []string `yaml:"functions"`
	// Aliases are the names of the aliases the action may change, in any
	// allowed function. The action changes aliases only, never $LATEST.
	// Required.
	Aliases []string `yaml:"aliases"`
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

	errs = append(errs, checkList("limits.functions", "function name or pattern", c.Limits.Functions, functionPattern)...)
	errs = append(errs, checkList("limits.aliases", "alias name", c.Limits.Aliases, aliasPattern)...)

	for i, alias := range c.Limits.Aliases {
		if allDigits.MatchString(alias) {
			errs = append(errs, fmt.Errorf("limits.aliases[%d]: %q is a version number, not an alias name", i, alias))
		}
	}

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

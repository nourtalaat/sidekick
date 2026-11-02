package prometheusrule

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nourtalaat/sidekick/internal/action"
)

// DefaultServiceAccount is the service account the action asks for tokens
// when the config names none. The chart creates it for the actions of this
// type.
const DefaultServiceAccount = "sidekick-actions"

// editableRoots are the fields of a rule that limits.fields may name. The
// others identify the rule (alert, record) or cannot be edited without
// adding or removing rules.
var editableRoots = []string{"expr", "for", "keep_firing_for", "labels", "annotations"}

// Config is the config section of one prometheus.rule action.
type Config struct {
	// ServiceAccount is the service account, in Sidekick's own namespace,
	// that the action gets short-lived tokens for. Empty means
	// DefaultServiceAccount. Give it get and patch on the PrometheusRules
	// of the limits only; the chart does this for the actions in the config.
	ServiceAccount string `yaml:"serviceAccount"`
	// Limits say which rules the action may change and which of their fields.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a prometheus.rule action. They are checked when
// the model proposes, again right before the change is written, and when
// it is undone.
type Limits struct {
	// Namespaces are the namespaces whose PrometheusRules the action may change.
	Namespaces []string `yaml:"namespaces"`
	// Names are the names of the PrometheusRules it may change, in each of
	// the namespaces. They are exact, because the Roles that the chart
	// makes list them as resource names.
	Names []string `yaml:"names"`
	// Groups are the names of the rule groups it may change.
	Groups []string `yaml:"groups"`
	// Fields are the fields of a rule the action may change, as paths such
	// as expr, for or labels.severity. A path allows the fields below it too.
	Fields []string `yaml:"fields"`
}

// Validate checks the section without network calls. Each error starts with
// the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.ServiceAccount != "" {
		for _, problem := range validation.IsDNS1123Subdomain(c.ServiceAccount) {
			errs = append(errs, fmt.Errorf("serviceAccount: %q is not a valid service account name: %s", c.ServiceAccount, problem))
		}
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// serviceAccount returns the service account name, with the default filled in.
func (c *Config) serviceAccount() string {
	if c.ServiceAccount == "" {
		return DefaultServiceAccount
	}

	return c.ServiceAccount
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	errs = append(errs, checkList("limits.namespaces", l.Namespaces, func(s string) []string { return validation.IsDNS1123Label(s) })...)
	errs = append(errs, checkList("limits.names", l.Names, func(s string) []string { return validation.IsDNS1123Subdomain(s) })...)
	errs = append(errs, checkList("limits.groups", l.Groups, groupProblems)...)

	if len(l.Fields) == 0 {
		errs = append(errs, errors.New("limits.fields: at least one field is required"))
	}

	for _, field := range l.Fields {
		if err := checkField(field); err != nil {
			errs = append(errs, fmt.Errorf("limits.fields: %w", err))
		}
	}

	return errs
}

// checkList checks that a list is not empty, holds no duplicates, and that
// problems finds nothing wrong with any entry.
func checkList(key string, list []string, problems func(string) []string) []error {
	if len(list) == 0 {
		return []error{fmt.Errorf("%s: at least one entry is required", key)}
	}

	var errs []error

	seen := map[string]bool{}

	for _, entry := range list {
		if seen[entry] {
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", key, entry))
		}

		seen[entry] = true

		if p := problems(entry); len(p) > 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not valid: %s", key, entry, strings.Join(p, "; ")))
		}
	}

	return errs
}

// groupProblems says what is wrong with a rule group name. A name that
// starts with a bracket would read as a position in a list of changes.
func groupProblems(name string) []string {
	switch {
	case strings.TrimSpace(name) == "":
		return []string{"it must not be empty"}
	case strings.HasPrefix(name, "["):
		return []string{"it must not start with a bracket"}
	default:
		return nil
	}
}

// checkField checks one entry of limits.fields: a valid path whose first
// key is a field of a rule that people may change.
func checkField(field string) error {
	path, err := action.ParsePath(field)
	if err != nil {
		return fmt.Errorf("check the path: %w", err)
	}

	if !slices.Contains(editableRoots, path[0]) {
		return fmt.Errorf("%q: the first key must be one of %s", field, strings.Join(editableRoots, ", "))
	}

	return nil
}

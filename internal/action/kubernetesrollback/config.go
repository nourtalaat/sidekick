package kubernetesrollback

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nourtalaat/sidekick/internal/action"
)

// defaultServiceAccount is the service account the action asks for tokens
// when the config names none. The chart creates it for the actions of this
// type.
const defaultServiceAccount = "sidekick-kubernetes-rollback"

// The kinds of workload the action can roll back.
const (
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
	kindDaemonSet   = "DaemonSet"
)

// allKinds are the kinds limits.kinds may name, and what an empty
// limits.kinds means.
var allKinds = []string{kindDeployment, kindStatefulSet, kindDaemonSet}

// Config is the config section of one kubernetes.rollback action. Embedding
// the gate makes the action approval-only, and embedding the revision limit
// adds maxRevisionsBack.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`

	// ServiceAccount is the service account, in Sidekick's own namespace,
	// that the action gets short-lived tokens for. Empty means
	// defaultServiceAccount. Give it get and patch on the workloads of the
	// limits only; the chart does this for the actions in the config.
	ServiceAccount string `yaml:"serviceAccount"`
	// Limits say which workloads the action may roll back.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a kubernetes.rollback action. They are checked
// when the model proposes, again right before the change is written, and
// when it is undone.
type Limits struct {
	// Namespaces are the namespaces whose workloads the action may roll back.
	Namespaces []string `yaml:"namespaces"`
	// Kinds are the kinds it may roll back: Deployment, StatefulSet,
	// DaemonSet. Empty means all three.
	Kinds []string `yaml:"kinds"`
	// Names are the names of the workloads it may roll back, in each of the
	// namespaces and of any of the kinds. They are exact, because the Roles
	// that the chart makes list them as resource names.
	Names []string `yaml:"names"`
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

	if err := c.CheckConfig(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// serviceAccount returns the service account name, with the default filled in.
func (c *Config) serviceAccount() string {
	if c.ServiceAccount == "" {
		return defaultServiceAccount
	}

	return c.ServiceAccount
}

// kinds returns the kinds the limits allow, with the default filled in.
func (l Limits) kinds() []string {
	if len(l.Kinds) == 0 {
		return allKinds
	}

	return l.Kinds
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	errs = append(errs, checkList("limits.namespaces", true, l.Namespaces, validation.IsDNS1123Label)...)
	errs = append(errs, checkList("limits.names", true, l.Names, validation.IsDNS1123Subdomain)...)
	errs = append(errs, checkList("limits.kinds", false, l.Kinds, kindProblems)...)

	return errs
}

// checkList checks that a required list is not empty, holds no duplicates,
// and that problems finds nothing wrong with any entry.
func checkList(key string, required bool, list []string, problems func(string) []string) []error {
	if len(list) == 0 && required {
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

// kindProblems says what is wrong with an entry of limits.kinds.
func kindProblems(kind string) []string {
	if slices.Contains(allKinds, kind) {
		return nil
	}

	return []string{"it must be one of " + strings.Join(allKinds, ", ")}
}

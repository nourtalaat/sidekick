package helmrollback

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	// defaultServiceAccount is the service account the action asks for
	// tokens when the config names none. The chart creates it.
	defaultServiceAccount = "sidekick-helm-rollback"
	// defaultWaitTimeout is how long a rollback waits for the objects when
	// waitTimeout is not set.
	defaultWaitTimeout = 3 * time.Minute
	// maxWaitTimeout is the longest waitTimeout. A run is cut off after 5
	// minutes, and Helm uses the timeout for the hooks and for the wait one
	// after the other, so the limit keeps most runs inside the cut-off.
	maxWaitTimeout = 4 * time.Minute

	// driverSecret and driverConfigMap are the places Helm keeps the records
	// of a release: Secrets, which is Helm's default, or ConfigMaps.
	driverSecret    = "secret"
	driverConfigMap = "configmap"

	// maxReleaseNameLength is the longest name Helm accepts for a release.
	maxReleaseNameLength = 53
)

// releasePattern is the shape of an entry of limits.releases: a release
// name in which "*" may stand for any run of characters. "*" is the only
// wildcard: the pattern holds no other character that a glob reads.
var releasePattern = regexp.MustCompile(`^[a-z0-9*]([a-z0-9.*-]{0,` + fmt.Sprint(maxReleaseNameLength-2) + `}[a-z0-9*])?$`)

// Config is the config section of one helm.rollback action. Embedding the
// gate makes the action approval-only, and the revision limit adds
// maxRevisionsBack.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`

	// ServiceAccount is the service account, in Sidekick's own namespace,
	// that the action gets short-lived tokens for. Empty means
	// defaultServiceAccount. The chart makes the account but gives it no
	// rights: a Helm rollback needs rights on every kind of object in the
	// chart, which the operator binds in each namespace.
	ServiceAccount string `yaml:"serviceAccount"`
	// Driver is where Helm keeps the release records in the cluster:
	// "secret" (the default) or "configmap".
	Driver string `yaml:"driver"`
	// Wait says whether a rollback waits until the objects are ready.
	// Unset means true.
	Wait *bool `yaml:"wait"`
	// WaitTimeout is how long a rollback waits, for hooks and for the
	// objects each. Unset means defaultWaitTimeout. At most maxWaitTimeout.
	WaitTimeout time.Duration `yaml:"waitTimeout"`
	// Limits say which releases the action may roll back.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a helm.rollback action. They are checked when
// the model proposes, again right before the rollback, and when it is
// undone.
type Limits struct {
	// Namespaces are the namespaces whose releases the action may roll back.
	Namespaces []string `yaml:"namespaces"`
	// Releases are the names of the releases it may roll back in each of the
	// namespaces: exact names, or patterns in which "*" stands for any run
	// of characters, such as checkout-*.
	Releases []string `yaml:"releases"`
}

// Validate checks the section without network calls. Each error starts with
// the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := c.CheckConfig(); err != nil {
		errs = append(errs, fmt.Errorf("check the revision limit: %w", err))
	}

	if c.ServiceAccount != "" {
		for _, problem := range validation.IsDNS1123Subdomain(c.ServiceAccount) {
			errs = append(errs, fmt.Errorf("serviceAccount: %q is not a valid service account name: %s", c.ServiceAccount, problem))
		}
	}

	if !slices.Contains([]string{"", driverSecret, driverConfigMap}, c.Driver) {
		errs = append(errs, fmt.Errorf("driver: %q must be %s or %s", c.Driver, driverSecret, driverConfigMap))
	}

	if c.WaitTimeout < 0 || c.WaitTimeout > maxWaitTimeout {
		errs = append(errs, fmt.Errorf("waitTimeout: %s must be above 0 and at most %s", c.WaitTimeout, maxWaitTimeout))
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

// driver returns where Helm keeps the records, with the default filled in.
func (c *Config) driver() string {
	if c.Driver == "" {
		return driverSecret
	}

	return c.Driver
}

// wait says whether a rollback waits for the objects.
func (c *Config) wait() bool {
	return c.Wait == nil || *c.Wait
}

// waitTimeout returns how long a rollback waits, with the default filled in.
func (c *Config) waitTimeout() time.Duration {
	if c.WaitTimeout == 0 {
		return defaultWaitTimeout
	}

	return c.WaitTimeout
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	errs = append(errs, checkList("limits.namespaces", l.Namespaces, validation.IsDNS1123Label)...)
	errs = append(errs, checkList("limits.releases", l.Releases, releaseProblems)...)

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

// releaseProblems says what is wrong with an entry of limits.releases.
func releaseProblems(entry string) []string {
	if releasePattern.MatchString(entry) {
		return nil
	}

	return []string{fmt.Sprintf("use at most %d lower case letters, digits, '-' and '.', starting and ending with a letter or digit, and '*' for any run of characters", maxReleaseNameLength)}
}

package azurecontainerapp

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/azureauth"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// maxAppPatternLen is the longest app name Azure allows, and so the longest
// pattern that can match one.
const maxAppPatternLen = 32

// maxResourceGroupLen is the longest resource group name Azure allows.
const maxResourceGroupLen = 90

// appPatternRe is the shape of an entry of limits.apps: lower-case letters,
// digits, "-" and "*", which stands for any run of characters.
var appPatternRe = regexp.MustCompile(`^[a-z0-9*-]+$`)

// resourceGroupRe is the shape of a resource group name.
var resourceGroupRe = regexp.MustCompile(`^[-\w.()]+$`)

// Config is the config section of one azure.container_app_rollback action.
// Embedding the gate makes the action approval-only.
type Config struct {
	action.ReadWriteGate `yaml:",inline"`
	action.RevisionLimit `yaml:",inline"`

	// TenantID is the Microsoft Entra tenant of the identity. Empty means
	// AZURE_TENANT_ID, which the AKS Workload Identity webhook sets.
	TenantID string `yaml:"tenantID"`
	// ClientID is the client ID of the identity. Empty means
	// AZURE_CLIENT_ID, which the webhook sets. Required with ClientSecret.
	ClientID string `yaml:"clientID"`
	// ClientSecret holds the client secret of an app registration, for
	// running outside AKS. Empty means AKS Workload Identity. Give the
	// action an identity of its own, apart from the one of the Azure
	// source.
	ClientSecret *registry.SecretRef `yaml:"clientSecret"`
	// Limits say which container apps the action may change.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of an azure.container_app_rollback action. They are
// checked when the model proposes, again right before the change is made,
// and when it is undone.
type Limits struct {
	// Subscriptions are the IDs of the subscriptions the action may touch.
	Subscriptions []string `yaml:"subscriptions"`
	// ResourceGroups are the names of the resource groups, in any of the
	// subscriptions, the action may touch.
	ResourceGroups []string `yaml:"resourceGroups"`
	// Apps are container app names, or patterns in which "*" stands for any
	// run of characters, such as checkout-*.
	Apps []string `yaml:"apps"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, c.identity().Check()...)

	if err := c.RevisionLimit.CheckConfig(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// identity returns the Entra identity of the config.
func (c *Config) identity() azureauth.Identity {
	return azureauth.Identity{TenantID: c.TenantID, ClientID: c.ClientID, ClientSecret: c.ClientSecret}
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	if len(l.Subscriptions) == 0 {
		errs = append(errs, errors.New("limits.subscriptions: list at least one subscription ID"))
	}

	errs = append(errs, checkList("limits.subscriptions", l.Subscriptions, "a GUID", azureauth.IsGUID)...)

	if len(l.ResourceGroups) == 0 {
		errs = append(errs, errors.New("limits.resourceGroups: list at least one resource group"))
	}

	errs = append(errs, checkList("limits.resourceGroups", l.ResourceGroups, "a resource group name", validResourceGroup)...)

	if len(l.Apps) == 0 {
		errs = append(errs, errors.New("limits.apps: list at least one app name or pattern"))
	}

	errs = append(errs, checkList("limits.apps", l.Apps, "an app name or pattern of lower-case letters, digits, - and *, at most 32 characters", validAppPattern)...)

	return errs
}

// checkList checks that every entry is valid, and that none is listed
// twice, ignoring case.
func checkList(key string, list []string, what string, valid func(string) bool) []error {
	var errs []error

	seen := map[string]bool{}

	for i, v := range list {
		switch {
		case !valid(v):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not %s", key, i, v, what))
		case seen[strings.ToLower(v)]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, v))
		}

		seen[strings.ToLower(v)] = true
	}

	return errs
}

// validResourceGroup reports whether s can be a resource group name.
func validResourceGroup(s string) bool {
	return len(s) <= maxResourceGroupLen && resourceGroupRe.MatchString(s)
}

// validAppPattern reports whether s can be an entry of limits.apps.
func validAppPattern(s string) bool {
	return len(s) <= maxAppPatternLen && appPatternRe.MatchString(s)
}

// allowsApp reports whether name, in lower case, matches an entry of
// limits.apps.
func (l Limits) allowsApp(name string) bool {
	for _, pattern := range l.Apps {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}

	return false
}

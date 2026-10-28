package azure

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// guidPattern is the shape of a subscription, tenant, client or workspace
// ID.
var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// workspacePattern is the shape of a Log Analytics workspace resource ID.
var workspacePattern = regexp.MustCompile(
	`(?i)^/subscriptions/[0-9a-f-]{36}/resourceGroups/[-\w.()]{1,90}/providers/Microsoft\.OperationalInsights/workspaces/([A-Za-z0-9-]{4,63})$`)

// Config is the config section of one Azure source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Subscriptions are the IDs of the subscriptions the tools may read. A
	// call for any other subscription, or a resource in one, is a tool
	// error, and nothing is sent.
	Subscriptions []string `yaml:"subscriptions"`
	// Workspaces are the Log Analytics workspaces query_logs may read.
	Workspaces []Workspace `yaml:"workspaces"`
	// TenantID is the Microsoft Entra tenant of the identity. Empty means
	// AZURE_TENANT_ID, which the AKS Workload Identity webhook sets.
	TenantID string `yaml:"tenantID"`
	// ClientID is the client ID of the identity. Empty means
	// AZURE_CLIENT_ID, which the webhook sets. Required with ClientSecret.
	ClientID string `yaml:"clientID"`
	// ClientSecret holds the client secret of an app registration, for
	// running outside AKS. Empty means AKS Workload Identity.
	ClientSecret *registry.SecretRef `yaml:"clientSecret"`
}

// Workspace is one Log Analytics workspace.
type Workspace struct {
	// ID is the workspace ID, the GUID the query API takes.
	ID string `yaml:"id"`
	// ResourceID is the workspace's Azure resource ID. Its last part is
	// the name the model calls the workspace by.
	ResourceID string `yaml:"resourceID"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Subscriptions) == 0 {
		errs = append(errs, errors.New("subscriptions: list at least one subscription ID"))
	}

	errs = append(errs, checkGUIDs("subscriptions", c.Subscriptions)...)
	errs = append(errs, c.checkWorkspaces()...)

	for _, f := range []struct{ key, value string }{{"tenantID", c.TenantID}, {"clientID", c.ClientID}} {
		if f.value != "" && !guidPattern.MatchString(f.value) {
			errs = append(errs, fmt.Errorf("%s: %q is not a GUID", f.key, f.value))
		}
	}

	if c.ClientSecret != nil && (c.TenantID == "" || c.ClientID == "") {
		errs = append(errs, errors.New("clientSecret: needs tenantID and clientID"))
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// checkWorkspaces checks that each workspace has a GUID and a workspace
// resource ID, and that no ID or name is listed twice.
func (c *Config) checkWorkspaces() []error {
	var errs []error

	ids := map[string]bool{}
	names := map[string]bool{}

	for i, w := range c.Workspaces {
		key := fmt.Sprintf("workspaces[%d]", i)

		if !guidPattern.MatchString(w.ID) {
			errs = append(errs, fmt.Errorf("%s.id: %q is not a workspace ID (a GUID)", key, w.ID))
		} else if ids[strings.ToLower(w.ID)] {
			errs = append(errs, fmt.Errorf("%s.id: %q is listed twice", key, w.ID))
		}

		ids[strings.ToLower(w.ID)] = true

		name := workspaceName(w.ResourceID)

		switch {
		case name == "":
			errs = append(errs, fmt.Errorf("%s.resourceID: %q is not a Log Analytics workspace resource ID", key, w.ResourceID))
		case names[name]:
			errs = append(errs, fmt.Errorf("%s.resourceID: another workspace is also named %q", key, name))
		}

		names[name] = true
	}

	return errs
}

// checkGUIDs checks that every value is a GUID, listed once.
func checkGUIDs(key string, values []string) []error {
	var errs []error

	seen := map[string]bool{}

	for i, v := range values {
		switch {
		case !guidPattern.MatchString(v):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not a GUID", key, i, v))
		case seen[strings.ToLower(v)]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, v))
		}

		seen[strings.ToLower(v)] = true
	}

	return errs
}

// workspaceName returns the lower-case name at the end of a workspace
// resource ID, or "" when it is not one.
func workspaceName(resourceID string) string {
	m := workspacePattern.FindStringSubmatch(resourceID)
	if m == nil {
		return ""
	}

	return strings.ToLower(m[1])
}

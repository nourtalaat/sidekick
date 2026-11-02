package datadog

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// DefaultSite is the Datadog site used when the config names none. The API
// is at api.<site>.
const DefaultSite = "datadoghq.com"

// editableRoots are the top-level fields of a monitor that limits.fields
// may name. The others are set by Datadog or cannot change after the
// monitor is made, such as its id, its type and its creator.
var editableRoots = []string{"name", "query", "message", "tags", "priority", "options"}

// siteHost is the shape of a Datadog site: a DNS name with a dot, such as
// datadoghq.com, datadoghq.eu or us3.datadoghq.com.
var siteHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// Config is the config section of one datadog.monitor action.
type Config struct {
	// Site is the Datadog site, such as datadoghq.eu. Empty means
	// DefaultSite. It cannot be set together with URL.
	Site string `yaml:"site"`
	// URL is the root of the API, for a proxy or a test server. It replaces
	// the address made from Site. It must be https, or http on a loopback
	// host.
	URL string `yaml:"url"`
	// APIKeySecret holds the API key. Give the action keys of its own, from
	// a service account that can write monitors only, apart from the keys of
	// any source that reads Datadog.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// AppKeySecret holds the application key of the same service account.
	AppKeySecret registry.SecretRef `yaml:"appKeySecret"`
	// Limits say which monitors the action may change and which fields.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a datadog.monitor action. They are checked when
// the model proposes, again right before the change is written, and when
// it is undone.
type Limits struct {
	// MonitorIDs are the monitors the action may change.
	MonitorIDs []int64 `yaml:"monitorIds"`
	// MonitorTags are tags: the action may also change a monitor that has
	// one of them, when it is read.
	MonitorTags []string `yaml:"monitorTags"`
	// Fields are the fields of a monitor the action may change, as paths
	// such as options.thresholds or message. A path allows the fields below
	// it too.
	Fields []string `yaml:"fields"`
	// AllowDelete lets the model propose to delete a monitor. A delete
	// cannot be undone in Sidekick. The default is false.
	AllowDelete bool `yaml:"allowDelete"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	switch {
	case c.Site != "" && c.URL != "":
		errs = append(errs, errors.New("url: cannot be set together with site"))
	case c.URL != "":
		if err := checkURL(c.URL); err != nil {
			errs = append(errs, err)
		}
	case c.Site != "" && !siteHost.MatchString(c.Site):
		errs = append(errs, fmt.Errorf("site: %q must be a Datadog site, such as datadoghq.com or datadoghq.eu", c.Site))
	}

	if c.APIKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("apiKeySecret: required"))
	}

	if c.AppKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("appKeySecret: required"))
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	if len(l.MonitorIDs) == 0 && len(l.MonitorTags) == 0 {
		errs = append(errs, errors.New("limits.monitorIds: at least one monitor ID, or limits.monitorTags: at least one tag, is required"))
	}

	for _, id := range l.MonitorIDs {
		if id < 1 {
			errs = append(errs, fmt.Errorf("limits.monitorIds: %d is not a monitor ID", id))
		}
	}

	for _, tag := range l.MonitorTags {
		if strings.TrimSpace(tag) == "" {
			errs = append(errs, errors.New("limits.monitorTags: a tag must not be empty"))
		}
	}

	if len(l.Fields) == 0 && !l.AllowDelete {
		errs = append(errs, errors.New("limits.fields: at least one field is required"))
	}

	for _, field := range l.Fields {
		if err := checkField(field); err != nil {
			errs = append(errs, fmt.Errorf("limits.fields: %w", err))
		}
	}

	return errs
}

// checkField checks one entry of limits.fields: a valid path whose first
// key is a field Datadog lets people change.
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

// checkURL checks that the URL is absolute and keeps the keys from crossing
// the network unencrypted: https, or http on a loopback host.
func checkURL(raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("url: %q must be an absolute https URL, such as https://api.datadoghq.eu", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("url: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// site returns the Datadog site, with the default filled in.
func (c *Config) site() string {
	if c.Site == "" {
		return DefaultSite
	}

	return c.Site
}

// apiURL returns the root of the API, without a trailing slash.
func (c *Config) apiURL() string {
	if c.URL != "" {
		return strings.TrimSuffix(c.URL, "/")
	}

	return "https://api." + c.site()
}

// appHost returns the host of the Datadog web app for a site, or "" when it
// is not known. Sites of two labels, such as datadoghq.com, use app.<site>;
// regional sites such as us3.datadoghq.com are the app host themselves.
func appHost(site string) string {
	if strings.Count(site, ".") == 1 {
		return "app." + site
	}

	return site
}

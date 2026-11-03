package datadog

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/datadogapi"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// editableRoots are the top-level fields of a monitor that limits.fields
// may name. The others are set by Datadog or cannot change after the
// monitor is made, such as its id, its type and its creator.
var editableRoots = []string{"name", "query", "message", "tags", "priority", "options"}

// Config is the config section of one datadog.monitor action.
type Config struct {
	// Site is the Datadog site, such as datadoghq.eu. Empty means
	// datadoghq.com. It cannot be set together with URL.
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
	errs := datadogapi.CheckConnection(c.Site, c.URL, c.APIKeySecret, c.AppKeySecret)

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

// apiURL returns the root of the API, without a trailing slash.
func (c *Config) apiURL() string {
	return datadogapi.APIURL(c.Site, c.URL)
}

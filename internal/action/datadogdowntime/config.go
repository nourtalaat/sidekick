package datadogdowntime

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action/datadogapi"
	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// maxScopeBytes is the longest scope or monitor tag a config may list.
const maxScopeBytes = 500

// wildcardTag is the monitor tag that makes Datadog mute every monitor, so
// no config may list it.
const wildcardTag = "*"

// Config is the config section of one datadog.downtime action.
type Config struct {
	// Site is the Datadog site, such as datadoghq.eu. Empty means
	// datadoghq.com. It cannot be set together with URL.
	Site string `yaml:"site"`
	// URL is the root of the API, for a proxy or a test server. It replaces
	// the address made from Site. It must be https, or http on a loopback
	// host.
	URL string `yaml:"url"`
	// APIKeySecret holds the API key. Give the action keys of its own, from
	// a service account that has the Monitors Downtime and Monitors Read
	// permissions and no others.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// AppKeySecret holds the application key of the same service account.
	AppKeySecret registry.SecretRef `yaml:"appKeySecret"`
	// Limits say which monitors may be muted, in which scopes, and for how
	// long.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a datadog.downtime action. They are checked when
// the model proposes and again right before the downtime is made.
type Limits struct {
	// MonitorIDs are the monitors the action may mute.
	MonitorIDs []int64 `yaml:"monitorIds"`
	// MonitorTags are tags: the action may also mute a monitor that has one
	// of them, and may mute by one of them.
	MonitorTags []string `yaml:"monitorTags"`
	// Scopes are the downtime scopes the model may use, each written exactly
	// as Datadog reads it, such as env:prod or host:web-1. Datadog's scope
	// syntax is a query language, so a scope is matched as a whole and never
	// by a pattern. At least one is required.
	Scopes []string `yaml:"scopes"`
	// MaxDuration is the longest downtime. Empty means
	// mute.DefaultMaxDuration; the most is mute.CeilingMaxDuration.
	MaxDuration time.Duration `yaml:"maxDuration"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	errs := datadogapi.CheckConnection(c.Site, c.URL, c.APIKeySecret, c.AppKeySecret)

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// apiURL returns the root of the API, without a trailing slash.
func (c *Config) apiURL() string {
	return datadogapi.APIURL(c.Site, c.URL)
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

	errs = append(errs, checkTexts("limits.monitorTags", l.MonitorTags, true)...)

	if len(l.Scopes) == 0 {
		errs = append(errs, errors.New("limits.scopes: at least one scope is required"))
	}

	errs = append(errs, checkTexts("limits.scopes", l.Scopes, false)...)

	if _, err := mute.MaxDuration(l.MaxDuration); err != nil {
		errs = append(errs, fmt.Errorf("limits.maxDuration: %w", err))
	}

	return errs
}

// checkTexts checks the entries of a list of scopes or tags: each is not
// empty, has no space at its ends, no control or format characters, is
// valid UTF-8 text of at most maxScopeBytes, and is listed once. Tags must
// not be the wildcard.
func checkTexts(key string, texts []string, tags bool) []error {
	var errs []error

	for i, text := range texts {
		switch {
		case strings.TrimSpace(text) == "":
			errs = append(errs, fmt.Errorf("%s[%d]: must not be empty", key, i))
		case text != strings.TrimSpace(text):
			errs = append(errs, fmt.Errorf("%s[%d]: %q has a space at its start or end", key, i, text))
		case len(text) > maxScopeBytes:
			errs = append(errs, fmt.Errorf("%s[%d]: longer than %d bytes", key, i, maxScopeBytes))
		case !utf8.ValidString(text):
			errs = append(errs, fmt.Errorf("%s[%d]: not valid UTF-8 text", key, i))
		case strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0:
			errs = append(errs, fmt.Errorf("%s[%d]: must not hold control characters", key, i))
		case tags && text == wildcardTag:
			errs = append(errs, fmt.Errorf("%s[%d]: %q would mute every monitor, so it cannot be a limit", key, i, wildcardTag))
		case slices.Index(texts, text) != i:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, text))
		}
	}

	return errs
}

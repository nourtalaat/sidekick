package signoz

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// DefaultKeyHeader is the header SigNoz reads its API key from, unless
// the operator changed it in the SigNoz config.
const DefaultKeyHeader = "SIGNOZ-API-KEY"

// ruleID is the shape of a rule ID: a UUID.
var ruleID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// headerName is the shape of an HTTP header name: a token.
var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// Config is the config section of one signoz.alert_rule action.
type Config struct {
	// URL is the address of SigNoz, such as https://example.signoz.cloud. It
	// must be https, or http on a loopback host. A path is kept, for a
	// SigNoz behind a proxy.
	URL string `yaml:"url"`
	// APIKeySecret holds the API key. Give the action a key of its own, from
	// a service account with the signoz-editor role, apart from the key of
	// any source that reads SigNoz.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// APIKeyHeader is the header SigNoz reads the key from. Empty means
	// DefaultKeyHeader.
	APIKeyHeader string `yaml:"apiKeyHeader"`
	// Limits say which rules the action may change and which fields.
	Limits Limits `yaml:"limits"`
}

// Limits are the limits of a signoz.alert_rule action. They are checked when
// the model proposes, again right before the change is written, and when it
// is undone.
type Limits struct {
	// RuleIDs are the rules the action may change, by ID.
	RuleIDs []string `yaml:"ruleIds"`
	// RuleNames are patterns of rule names: the action may also change a
	// rule whose name matches one. "*" is the only wildcard, the pattern
	// must match the whole name, and the match is case sensitive.
	RuleNames []string `yaml:"ruleNames"`
	// Fields are the fields of a rule the action may change, as paths such
	// as condition.thresholds or labels. A path allows the fields below it
	// too.
	Fields []string `yaml:"fields"`
	// AllowDelete lets the model propose to delete a rule. A delete cannot
	// be undone in Sidekick. The default is false.
	AllowDelete bool `yaml:"allowDelete"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errors.New("url: required, the address of SigNoz such as https://example.signoz.cloud"))
	} else if err := checkURL(c.URL); err != nil {
		errs = append(errs, err)
	}

	if c.APIKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("apiKeySecret: required"))
	}

	if c.APIKeyHeader != "" && !headerName.MatchString(c.APIKeyHeader) {
		errs = append(errs, fmt.Errorf("apiKeyHeader: %q is not an HTTP header name", c.APIKeyHeader))
	}

	errs = append(errs, c.Limits.check()...)

	return errors.Join(errs...)
}

// check returns the problems with the limits.
func (l Limits) check() []error {
	var errs []error

	if len(l.RuleIDs) == 0 && len(l.RuleNames) == 0 {
		errs = append(errs, errors.New("limits.ruleIds: at least one rule ID, or limits.ruleNames: at least one name pattern, is required"))
	}

	for _, id := range l.RuleIDs {
		if !ruleID.MatchString(id) {
			errs = append(errs, fmt.Errorf("limits.ruleIds: %q is not a rule ID, which is a UUID", id))
		}
	}

	for _, pattern := range l.RuleNames {
		if strings.TrimSpace(pattern) == "" {
			errs = append(errs, errors.New("limits.ruleNames: a pattern must not be empty"))
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

// checkField checks one entry of limits.fields: a valid path that is a field
// of a rule Sidekick can change, or is above some.
func checkField(field string) error {
	path, err := action.ParsePath(field)
	if err != nil {
		return fmt.Errorf("check the path: %w", err)
	}

	if !fieldKnown(path) {
		return fmt.Errorf("%q is not a field Sidekick can change in a SigNoz rule; they are %s", field, strings.Join(supportedWithin([]action.Path{{}}), ", "))
	}

	return nil
}

// checkURL checks that the URL is absolute and keeps the key from crossing
// the network unencrypted: https, or http on a loopback host.
func checkURL(raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("url: %q must be an absolute https URL, such as https://example.signoz.cloud", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("url: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// apiURL returns the address of SigNoz, without a trailing slash.
func (c *Config) apiURL() string {
	return strings.TrimSuffix(c.URL, "/")
}

// keyHeader returns the header the API key goes in.
func (c *Config) keyHeader() string {
	if c.APIKeyHeader == "" {
		return DefaultKeyHeader
	}

	return c.APIKeyHeader
}

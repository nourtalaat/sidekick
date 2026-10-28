package elasticsearch

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Flavors of the search server.
const (
	FlavorElasticsearch = "elasticsearch"
	FlavorOpenSearch    = "opensearch"
)

// Defaults of the field keys.
const (
	defaultTimeField    = "@timestamp"
	defaultMessageField = "message"
)

// defaultFieldsBesides are the fields search_logs returns, next to the time
// and message fields, when neither the call nor the config names any. They
// are Elastic Common Schema names; a field a document lacks is left out.
var defaultFieldsBesides = []string{"log.level", "service.name", "host.name", "error.message", "trace.id"}

// sampleLink fills the discoverURL template when the config is checked.
var sampleLink = linkData{
	Index: "logs-*",
	Query: "level:error",
	From:  time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC),
	To:    time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
}

// Config is the config section of one Elasticsearch or OpenSearch source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Flavor is elasticsearch or opensearch. Empty means elasticsearch.
	Flavor string `yaml:"flavor"`
	// BaseURL is the root of the cluster's REST API, for example
	// https://logs.example.com:9200.
	BaseURL string `yaml:"baseURL"`
	// APIKeySecret holds an Elasticsearch API key in its encoded form, sent
	// as "Authorization: ApiKey <key>". Only with the elasticsearch flavor.
	APIKeySecret *registry.SecretRef `yaml:"apiKeySecret"`
	// BasicAuth is a user name and the secret that holds its password.
	BasicAuth *BasicAuth `yaml:"basicAuth"`
	// IndexPatterns are the index patterns the tools may read, such as
	// logs-*. A call for an index or pattern that none of them covers is a
	// tool error, and nothing is sent.
	IndexPatterns []string `yaml:"indexPatterns"`
	// TimeField is the field that holds each document's time. Empty means
	// @timestamp.
	TimeField string `yaml:"timeField"`
	// MessageField is the field that holds the log message. Empty means
	// message.
	MessageField string `yaml:"messageField"`
	// DefaultFields are the fields search_logs returns when the call names
	// none, next to the time and message fields. Empty means a few Elastic
	// Common Schema fields such as log.level and service.name.
	DefaultFields []string `yaml:"defaultFields"`
	// DiscoverURL is a Go template for evidence links to a search in Kibana
	// Discover or OpenSearch Dashboards, with the fields Index, Query, From
	// and To, each escaped for a URL. Empty means evidence without a link.
	DiscoverURL string `yaml:"discoverURL"`
}

// BasicAuth is a user name and the secret that holds its password.
type BasicAuth struct {
	Username       string             `yaml:"username"`
	PasswordSecret registry.SecretRef `yaml:"passwordSecret"`
}

// Validate checks the section without secrets or network calls: the
// flavor is known, the base URL is an absolute https URL, exactly one way
// to authenticate is set, the index patterns and field names are valid,
// discoverURL renders an absolute URL, and the timeout fits the HTTP
// client. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.Flavor != "" && c.Flavor != FlavorElasticsearch && c.Flavor != FlavorOpenSearch {
		errs = append(errs, fmt.Errorf("flavor: %q must be %s or %s", c.Flavor, FlavorElasticsearch, FlavorOpenSearch))
	}

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, c.checkAuth()...)
	errs = append(errs, checkPatterns(c.IndexPatterns)...)

	if c.TimeField != "" && checkField(c.TimeField, false) != nil {
		errs = append(errs, fmt.Errorf("timeField: %q is not a field name", c.TimeField))
	}

	if c.MessageField != "" && checkField(c.MessageField, false) != nil {
		errs = append(errs, fmt.Errorf("messageField: %q is not a field name", c.MessageField))
	}

	if len(c.DefaultFields) > maxFields {
		errs = append(errs, fmt.Errorf("defaultFields: list at most %d fields", maxFields))
	}

	for i, f := range c.DefaultFields {
		if checkField(f, true) != nil {
			errs = append(errs, fmt.Errorf("defaultFields[%d]: %q is not a field name", i, f))
		}
	}

	if c.DiscoverURL != "" {
		if _, err := parseDiscoverURL(c.DiscoverURL); err != nil {
			errs = append(errs, fmt.Errorf("discoverURL: %w", err))
		}
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// checkAuth checks that exactly one of apiKeySecret and basicAuth is set,
// and that the one set is complete.
func (c *Config) checkAuth() []error {
	switch {
	case c.APIKeySecret == nil && c.BasicAuth == nil:
		return []error{errors.New("apiKeySecret or basicAuth: set one, so Sidekick can sign in")}
	case c.APIKeySecret != nil && c.BasicAuth != nil:
		return []error{errors.New("apiKeySecret and basicAuth: set only one")}
	case c.APIKeySecret != nil && c.flavor() == FlavorOpenSearch:
		return []error{errors.New("apiKeySecret: only with flavor elasticsearch; use basicAuth for OpenSearch")}
	case c.APIKeySecret != nil && *c.APIKeySecret == (registry.SecretRef{}):
		return []error{errors.New("apiKeySecret: name the secret and key")}
	case c.APIKeySecret != nil:
		return nil
	}

	var errs []error

	if strings.TrimSpace(c.BasicAuth.Username) == "" || strings.Contains(c.BasicAuth.Username, ":") {
		errs = append(errs, errors.New("basicAuth.username: required, without a colon"))
	}

	if c.BasicAuth.PasswordSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("basicAuth.passwordSecret: required"))
	}

	return errs
}

// flavor returns the flavor, with the default applied.
func (c *Config) flavor() string {
	if c.Flavor == "" {
		return FlavorElasticsearch
	}

	return c.Flavor
}

// product names the flavor for a person, in messages and evidence titles.
func (c *Config) product() string {
	if c.flavor() == FlavorOpenSearch {
		return "OpenSearch"
	}

	return "Elasticsearch"
}

// timeField returns the time field, with the default applied.
func (c *Config) timeField() string {
	if c.TimeField == "" {
		return defaultTimeField
	}

	return c.TimeField
}

// messageField returns the message field, with the default applied.
func (c *Config) messageField() string {
	if c.MessageField == "" {
		return defaultMessageField
	}

	return c.MessageField
}

// defaultFields returns the fields search_logs returns when the call names
// none: the time and message fields first, then the configured or default
// ones, each once.
func (c *Config) defaultFields() []string {
	rest := c.DefaultFields
	if len(rest) == 0 {
		rest = defaultFieldsBesides
	}

	return withTimeAndMessage(c.timeField(), c.messageField(), rest)
}

// apiURL returns the root of the REST API, without a trailing slash.
func (c *Config) apiURL() string {
	return strings.TrimSuffix(c.BaseURL, "/")
}

// checkBaseURL checks that the base URL is set and is an absolute https
// URL, so the credentials never cross the network unencrypted. Plain http
// is allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return errors.New("baseURL: required, for example https://logs.example.com:9200")
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, without a query or user", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// checkPatterns checks the allowlist: at least one pattern, each a valid
// index pattern that names something more than every index, listed once.
func checkPatterns(patterns []string) []error {
	if len(patterns) == 0 {
		return []error{errors.New("indexPatterns: list at least one index pattern, such as logs-*")}
	}

	var errs []error

	seen := map[string]bool{}

	for i, p := range patterns {
		key := fmt.Sprintf("indexPatterns[%d]", i)

		switch {
		case !validIndex(p):
			errs = append(errs, fmt.Errorf("%s: %q is not an index pattern: use lower-case letters, digits, '.', '_', '-' and '*'", key, p))
		case strings.Trim(p, "*.") == "":
			errs = append(errs, fmt.Errorf("%s: %q matches every index; name the indexes Sidekick may read, such as logs-*", key, p))
		case seen[p]:
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", key, p))
		}

		seen[p] = true
	}

	return errs
}

// parseDiscoverURL parses the discoverURL template and checks that it
// renders an absolute http or https URL for a sample search.
func parseDiscoverURL(text string) (*template.Template, error) {
	tmpl, err := template.New("discoverURL").Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse the template: %w", err)
	}

	link, err := renderLink(tmpl, sampleLink)
	if err != nil {
		return nil, fmt.Errorf("render the template: %w", err)
	}

	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q must render an absolute http or https URL, not %q", text, link)
	}

	return tmpl, nil
}

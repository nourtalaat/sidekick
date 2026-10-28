package launchdarkly

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// DefaultBaseURL is the root of LaunchDarkly's commercial instance. The EU
// instance is https://app.eu.launchdarkly.com and the Federal one is
// https://app.launchdarkly.us.
const DefaultBaseURL = "https://app.launchdarkly.com"

// keyPattern is the shape of a LaunchDarkly project, environment or flag
// key. It holds no '*', ':', ';', ',' or '/', so a key can go into a
// request path or a resource specifier without changing what it names.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

// Config is the config section of one LaunchDarkly source.
type Config struct {
	source.Settings `yaml:",inline"`

	// TokenSecret holds the access token Sidekick sends. A token with the
	// Reader role is enough.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Projects are the projects, and the environments in each, the tools
	// may read. A call for anything else is a tool error, and nothing is
	// sent.
	Projects flags.Allowlist `yaml:"projects"`
	// BaseURL is the root of the LaunchDarkly instance, for the EU or
	// Federal instance. Empty means DefaultBaseURL.
	BaseURL string `yaml:"baseURL"`
}

// Validate checks the section without secrets or network calls: the token
// secret is named, projects lists valid keys, the base URL, when set, is
// an absolute https URL, and the timeout fits the HTTP client. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if err := c.Projects.Check(keyPattern); err != nil {
		errs = append(errs, err)
	}

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// baseURL returns the root of the instance, without a trailing slash.
func (c *Config) baseURL() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}

	return strings.TrimSuffix(c.BaseURL, "/")
}

// checkBaseURL checks that the base URL, when set, is an absolute https
// URL, so the token never crosses the network unencrypted. Plain http is
// allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://app.eu.launchdarkly.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

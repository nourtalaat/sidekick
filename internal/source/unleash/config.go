package unleash

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

// keyPattern is the shape of an Unleash project, environment or flag key.
// It holds no ':', ',' or '/', so a key can go into a request path or an
// event filter without changing what it names, and it starts with a letter
// or digit, so it is never '.' or '..' in a path.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)

// Config is the config section of one Unleash source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the root of the Unleash instance, such as
	// https://unleash.example.com. It may hold a path, for an instance
	// served under one. Required.
	BaseURL string `yaml:"baseURL"`
	// TokenSecret holds the service account token Sidekick sends. A token
	// of a service account with the Viewer role is enough.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Projects are the projects, and the environments in each, the tools
	// may read. A call for anything else is a tool error, and nothing is
	// sent. Unleash's default project is "default".
	Projects flags.Allowlist `yaml:"projects"`
}

// Validate checks the section without secrets or network calls: the base
// URL is an absolute https URL, the token secret is named, projects lists
// valid keys, and the timeout fits the HTTP client. Each error starts with
// the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if err := c.Projects.Check(keyPattern); err != nil {
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
	return strings.TrimSuffix(c.BaseURL, "/")
}

// checkBaseURL checks that the base URL is set and is an absolute https
// URL, so the token never crosses the network unencrypted. Plain http is
// allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return errors.New("baseURL: required, for example https://unleash.example.com")
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://unleash.example.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

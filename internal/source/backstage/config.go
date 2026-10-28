package backstage

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultNamespace is the catalog namespace of a name given without one.
const defaultNamespace = "default"

// Config is the config section of one Backstage source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the root of the Backstage backend, for example
	// https://backstage.example.com. The catalog API is under /api/catalog.
	BaseURL string `yaml:"baseURL"`
	// AppURL is the root of the Backstage web app, for the links to entity
	// pages. Empty means BaseURL, for the usual setup where one host serves
	// both.
	AppURL string `yaml:"appURL"`
	// TokenSecret holds the static token Sidekick sends as a bearer token.
	TokenSecret registry.SecretRef `yaml:"tokenSecret"`
	// Namespace is the catalog namespace of a name given without one.
	// Empty means "default".
	Namespace string `yaml:"namespace"`
}

// Validate checks the section without secrets or network calls: the base
// URL is an absolute https URL, the app URL, when set, is an absolute URL,
// the token secret is named, the namespace looks like one, and the timeout
// fits the HTTP client. Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if err := checkBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}

	if err := checkAppURL(c.AppURL); err != nil {
		errs = append(errs, err)
	}

	if c.TokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errors.New("tokenSecret: required"))
	}

	if c.Namespace != "" && !validName(c.Namespace) {
		errs = append(errs, fmt.Errorf("namespace: %q is not a valid catalog namespace", c.Namespace))
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// apiURL returns the root of the catalog API, without a trailing slash.
func (c *Config) apiURL() string {
	return strings.TrimSuffix(c.BaseURL, "/") + "/api/catalog"
}

// appURL returns the root of the web app, without a trailing slash.
func (c *Config) appURL() string {
	if c.AppURL == "" {
		return strings.TrimSuffix(c.BaseURL, "/")
	}

	return strings.TrimSuffix(c.AppURL, "/")
}

// namespace returns the namespace of names given without one.
func (c *Config) namespace() string {
	if c.Namespace == "" {
		return defaultNamespace
	}

	return c.Namespace
}

// checkBaseURL checks that the base URL is set and is an absolute https
// URL, so the token never crosses the network unencrypted. Plain http is
// allowed only for a loopback host, such as a local test server.
func checkBaseURL(raw string) error {
	if raw == "" {
		return errors.New("baseURL: required, for example https://backstage.example.com")
	}

	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("baseURL: %q must be an absolute https URL", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// checkAppURL checks that the app URL, when set, is an absolute http or
// https URL. No token is sent to it; it only makes links.
func checkAppURL(raw string) error {
	if raw == "" {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("appURL: %q must be an absolute http or https URL", raw)
	}

	return nil
}

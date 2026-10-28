package prometheus

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// headerNamePattern is the HTTP token syntax of a header name.
var headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// datasourcePattern is the shape of a Grafana data source UID.
var datasourcePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// credentialHeaders carry credentials, which come only from Secrets
// through bearerTokenSecret, basicAuth and tls, never from headers.
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie"}

// ownHeaders are set by the source itself.
var ownHeaders = []string{"Accept", "Content-Type", "Content-Length", "Host", "User-Agent"}

// Config is the config section of one Prometheus source.
type Config struct {
	source.Settings `yaml:",inline"`

	// BaseURL is the root of the Prometheus HTTP API, the part before
	// /api/v1, for example https://prometheus.example.com, or
	// https://mimir.example.com/prometheus for Mimir.
	BaseURL string `yaml:"baseURL"`
	// AlertmanagerURL is the root of an Alertmanager API, the part before
	// /api/v2. When it is set, list_alerts reads Alertmanager, with
	// silences, instead of Prometheus. The credentials and headers go to
	// it too.
	AlertmanagerURL string `yaml:"alertmanagerURL"`
	// BearerTokenSecret holds a token sent as a bearer token.
	BearerTokenSecret *registry.SecretRef `yaml:"bearerTokenSecret"`
	// BasicAuth is a user name and the secret that holds its password.
	BasicAuth *BasicAuth `yaml:"basicAuth"`
	// TLS is a client certificate for mutual TLS, and a CA to trust.
	TLS *TLS `yaml:"tls"`
	// Headers are sent with every request, for example X-Scope-OrgID for a
	// Mimir or Cortex tenant. They hold no credentials: those come from
	// Secrets.
	Headers map[string]string `yaml:"headers"`
	// Links says where evidence links point.
	Links Links `yaml:"links"`
}

// BasicAuth is the user name and password of HTTP basic auth.
type BasicAuth struct {
	// Username is the user name.
	Username string `yaml:"username"`
	// PasswordSecret holds the password.
	PasswordSecret registry.SecretRef `yaml:"passwordSecret"`
}

// TLS is the TLS setup of the connections.
type TLS struct {
	// CertSecret holds a PEM client certificate, sent for mutual TLS.
	CertSecret *registry.SecretRef `yaml:"certSecret"`
	// KeySecret holds the PEM private key of the client certificate.
	KeySecret *registry.SecretRef `yaml:"keySecret"`
	// CASecret holds PEM CA certificates to trust instead of the system
	// ones, for a server with a private CA.
	CASecret *registry.SecretRef `yaml:"caSecret"`
}

// Links are the roots of the web UIs that evidence links open.
type Links struct {
	// PrometheusURL is the root of the Prometheus web UI. Empty means
	// BaseURL.
	PrometheusURL string `yaml:"prometheusURL"`
	// GrafanaURL is the root of a Grafana. When it is set, queries link to
	// Grafana Explore instead of the Prometheus web UI.
	GrafanaURL string `yaml:"grafanaURL"`
	// GrafanaDatasource is the UID of the Grafana data source that reads
	// this server. Required with GrafanaURL.
	GrafanaDatasource string `yaml:"grafanaDatasource"`
}

// Validate checks the section without secrets or network calls. Each error
// starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	credentials := c.sendsCredentials()

	if c.BaseURL == "" {
		errs = append(errs, errors.New("baseURL: required, for example https://prometheus.example.com"))
	} else if err := checkAPIURL("baseURL", c.BaseURL, credentials); err != nil {
		errs = append(errs, err)
	}

	if c.AlertmanagerURL != "" {
		if err := checkAPIURL("alertmanagerURL", c.AlertmanagerURL, credentials); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, c.checkAuth()...)
	errs = append(errs, c.checkHeaders()...)
	errs = append(errs, c.Links.check()...)

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// sendsCredentials reports whether requests carry a credential: a token, a
// password or a client certificate.
func (c *Config) sendsCredentials() bool {
	return c.BearerTokenSecret != nil || c.BasicAuth != nil || (c.TLS != nil && c.TLS.CertSecret != nil)
}

// usesSecrets reports whether the source reads any secret.
func (c *Config) usesSecrets() bool {
	return c.sendsCredentials() || (c.TLS != nil && c.TLS.CASecret != nil)
}

// checkAuth checks that at most one of the bearer token and basic auth is
// set, that basic auth is complete, and that the client certificate has
// its key.
func (c *Config) checkAuth() []error {
	var errs []error

	if c.BearerTokenSecret != nil && c.BasicAuth != nil {
		errs = append(errs, errors.New("bearerTokenSecret: set one of bearerTokenSecret and basicAuth, not both"))
	}

	if c.BasicAuth != nil {
		if c.BasicAuth.Username == "" {
			errs = append(errs, errors.New("basicAuth.username: required"))
		}

		if c.BasicAuth.PasswordSecret == (registry.SecretRef{}) {
			errs = append(errs, errors.New("basicAuth.passwordSecret: required"))
		}
	}

	if c.TLS != nil {
		switch {
		case (c.TLS.CertSecret == nil) != (c.TLS.KeySecret == nil):
			errs = append(errs, errors.New("tls: set both certSecret and keySecret, or neither"))
		case c.TLS.CertSecret == nil && c.TLS.CASecret == nil:
			errs = append(errs, errors.New("tls: set certSecret and keySecret, caSecret, or both"))
		}

		for _, f := range []struct{ key, raw string }{{"baseURL", c.BaseURL}, {"alertmanagerURL", c.AlertmanagerURL}} {
			if strings.HasPrefix(f.raw, "http://") {
				errs = append(errs, fmt.Errorf("tls: needs https, but %s uses http", f.key))
			}
		}
	}

	return errs
}

// checkHeaders checks that each header has a valid name and value, carries
// no credential and is not one the source sets itself.
func (c *Config) checkHeaders() []error {
	var errs []error

	for _, name := range sortedKeys(c.Headers) {
		key := "headers." + name
		canonical := http.CanonicalHeaderKey(name)

		switch {
		case !headerNamePattern.MatchString(name):
			errs = append(errs, fmt.Errorf("%s: %q is not a valid header name", key, name))
		case slices.Contains(credentialHeaders, canonical):
			errs = append(errs, fmt.Errorf("%s: credentials come from Secrets; use bearerTokenSecret, basicAuth or tls", key))
		case slices.Contains(ownHeaders, canonical):
			errs = append(errs, fmt.Errorf("%s: Sidekick sets this header itself", key))
		case !validHeaderValue(c.Headers[name]):
			errs = append(errs, fmt.Errorf("%s: the value must not hold control characters such as line breaks", key))
		}
	}

	return errs
}

// prometheusURL returns the root of the Prometheus web UI, without a
// trailing slash.
func (c *Config) prometheusURL() string {
	if c.Links.PrometheusURL != "" {
		return strings.TrimSuffix(c.Links.PrometheusURL, "/")
	}

	return strings.TrimSuffix(c.BaseURL, "/")
}

// check checks the link roots: absolute http or https URLs, and a data
// source UID with the Grafana URL. No credential is sent to them; they
// only make links.
func (l Links) check() []error {
	var errs []error

	for _, f := range []struct{ key, raw string }{{"links.prometheusURL", l.PrometheusURL}, {"links.grafanaURL", l.GrafanaURL}} {
		if f.raw == "" {
			continue
		}

		if u, err := url.Parse(f.raw); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, fmt.Errorf("%s: %q must be an absolute http or https URL", f.key, f.raw))
		}
	}

	switch {
	case l.GrafanaURL != "" && l.GrafanaDatasource == "":
		errs = append(errs, errors.New("links.grafanaDatasource: required with links.grafanaURL"))
	case l.GrafanaDatasource != "" && l.GrafanaURL == "":
		errs = append(errs, errors.New("links.grafanaDatasource: only with links.grafanaURL"))
	case l.GrafanaDatasource != "" && !datasourcePattern.MatchString(l.GrafanaDatasource):
		errs = append(errs, fmt.Errorf("links.grafanaDatasource: %q is not a Grafana data source UID", l.GrafanaDatasource))
	}

	return errs
}

// checkAPIURL checks that an API root is an absolute http or https URL
// with no query or fragment. When credentials are sent it must use https,
// so they never cross the network unencrypted; plain http is then allowed
// only for a loopback host, such as a local test server.
func checkAPIURL(key, raw string, credentials bool) error {
	u, err := url.Parse(raw)

	switch {
	case err == nil && u.User != nil:
		return fmt.Errorf("%s: must not hold a user name or password; put them in basicAuth, with the password in a Secret", key)
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "":
		return fmt.Errorf("%s: %q must be an absolute http or https URL", key, raw)
	case credentials && u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("%s: %q must use https, because credentials are sent; plain http is only allowed for localhost", key, raw)
	default:
		return nil
	}
}

// validHeaderValue reports whether v holds no control characters other
// than tabs.
func validHeaderValue(v string) bool {
	for _, r := range v {
		if (r < ' ' && r != '\t') || r == 0x7f {
			return false
		}
	}

	return true
}

// sortedKeys returns the keys of m in order, so errors come out the same
// way every time.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

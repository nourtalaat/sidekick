package prometheus

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// datasourcePattern is the shape of a Grafana data source UID.
var datasourcePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

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
type BasicAuth = httpauth.BasicAuth

// TLS is the TLS setup of the connections.
type TLS = httpauth.TLS

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

	credentials := c.credentials()
	sends := credentials.SendsCredentials()

	if c.BaseURL == "" {
		errs = append(errs, errors.New("baseURL: required, for example https://prometheus.example.com"))
	} else if err := httpauth.CheckAPIURL("baseURL", c.BaseURL, sends); err != nil {
		errs = append(errs, err)
	}

	if c.AlertmanagerURL != "" {
		if err := httpauth.CheckAPIURL("alertmanagerURL", c.AlertmanagerURL, sends); err != nil {
			errs = append(errs, err)
		}
	}

	errs = append(errs, credentials.Check(httpauth.URL{Key: "baseURL", Raw: c.BaseURL}, httpauth.URL{Key: "alertmanagerURL", Raw: c.AlertmanagerURL})...)
	errs = append(errs, c.Links.check()...)

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers",
			c.Timeout, httpclient.ResponseHeaderTimeout))
	}

	return errors.Join(errs...)
}

// credentials returns the credential and header keys of the config.
func (c *Config) credentials() httpauth.Config {
	return httpauth.Config{BearerTokenSecret: c.BearerTokenSecret, BasicAuth: c.BasicAuth, TLS: c.TLS, Headers: c.Headers}
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

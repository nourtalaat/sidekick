package httpauth

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
)

// headerNamePattern is the HTTP token syntax of a header name.
var headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// credentialHeaders carry credentials, which come only from Secrets
// through bearerTokenSecret, basicAuth and tls, never from headers.
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie"}

// ownHeaders are set by the caller itself.
var ownHeaders = []string{"Accept", "Content-Type", "Content-Length", "Host", "User-Agent"}

// Config is the credential and header keys of a config section that calls
// an HTTP API. A config struct can declare these fields itself or embed
// Config with `yaml:",inline"`.
type Config struct {
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
}

// BasicAuth is the user name and password of HTTP basic auth.
type BasicAuth struct {
	// Username is the user name.
	Username string `yaml:"username"`
	// PasswordSecret holds the password.
	PasswordSecret registry.SecretRef `yaml:"passwordSecret"`
}

// URL is a configured address, with the key it is written under, for the
// error messages of Config.Check.
type URL struct {
	Key string
	Raw string
}

// SendsCredentials reports whether requests carry a credential: a token, a
// password or a client certificate.
func (c Config) SendsCredentials() bool {
	return c.BearerTokenSecret != nil || c.BasicAuth != nil || (c.TLS != nil && c.TLS.CertSecret != nil)
}

// UsesSecrets reports whether the config reads any secret.
func (c Config) UsesSecrets() bool {
	return c.SendsCredentials() || (c.TLS != nil && c.TLS.CASecret != nil)
}

// Check checks, without secrets or network calls, that at most one of the
// bearer token and basic auth is set, that basic auth is complete, that the
// client certificate has its key, that tls goes with https URLs only, and
// that each header has a valid name and value, carries no credential and is
// not one the caller sets itself. Each error starts with the key of its
// field.
func (c Config) Check(urls ...URL) []error {
	return append(c.checkAuth(urls), c.checkHeaders()...)
}

// checkAuth checks the bearer token, basic auth and tls keys.
func (c Config) checkAuth(urls []URL) []error {
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

		for _, u := range urls {
			if strings.HasPrefix(u.Raw, "http://") {
				errs = append(errs, fmt.Errorf("tls: needs https, but %s uses http", u.Key))
			}
		}
	}

	return errs
}

// checkHeaders checks the names and values of the headers.
func (c Config) checkHeaders() []error {
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

// CheckAPIURL checks that an API root is an absolute http or https URL
// with no query or fragment. When credentials are sent it must use https,
// so they never cross the network unencrypted; plain http is then allowed
// only for a loopback host, such as a local test server.
func CheckAPIURL(key, raw string, credentials bool) error {
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

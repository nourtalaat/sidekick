package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Transport is how Sidekick talks to an MCP server.
type Transport string

// Transports.
const (
	// TransportHTTP is streamable HTTP, the default.
	TransportHTTP Transport = "http"
	// TransportSSE is the legacy HTTP with server-sent events transport,
	// for older servers.
	TransportSSE Transport = "sse"
	// TransportStdio starts the server as a child process and talks to it
	// over its standard input and output.
	TransportStdio Transport = "stdio"
)

// headerNamePattern is the HTTP token syntax of a header name.
var headerNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// envNamePattern is the shape of an environment variable name.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// errOAuthUnsupported is returned for a source that sets oauth.
var errOAuthUnsupported = errors.New("oauth: MCP sources do not support OAuth yet; " +
	"put a token in a Secret and send it with headers (or env for stdio), see docs/sources/mcp-authentication.md")

// reservedHeaders are set by the MCP transports themselves, so config may
// not set them.
var reservedHeaders = []string{"Accept", "Content-Type", "Content-Length", "Host", "Last-Event-Id", "Mcp-Protocol-Version", "Mcp-Session-Id"}

// Config is the config section of one MCP source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Transport is http (the default), sse or stdio.
	Transport Transport `yaml:"transport"`
	// URL is the server's endpoint, for http and sse.
	URL string `yaml:"url"`
	// Headers are sent with every HTTP request, for example a bearer token.
	Headers map[string]Value `yaml:"headers"`
	// Command is the program and its arguments, for stdio.
	Command []string `yaml:"command"`
	// Env is the environment of the stdio child process. The child gets
	// only these variables and PATH, never Sidekick's own environment.
	Env map[string]Value `yaml:"env"`
	// OAuth is read only so that Validate can reject it with a clear
	// message: MCP sources authenticate with static headers or env values
	// from Secrets, and do not support OAuth yet.
	OAuth yaml.Node `yaml:"oauth"`
}

// Value is a header or environment value: a plain value, or a secret with
// an optional prefix such as "Bearer ".
type Value struct {
	Value  string              `yaml:"value"`
	Secret *registry.SecretRef `yaml:"secret"`
	Prefix string              `yaml:"prefix"`
}

// Validate checks the section without secrets or network calls: http and
// sse need an absolute http or https url and a timeout the HTTP client can
// honour, stdio needs a command, each header and environment value is set
// exactly one way, and oauth is not set. Each error starts with the key of
// its field.
func (c *Config) Validate() error {
	var errs []error

	if c.OAuth.Kind != 0 {
		errs = append(errs, errOAuthUnsupported)
	}

	switch c.transport() {
	case TransportHTTP, TransportSSE:
		errs = append(errs, c.checkHTTP()...)
	case TransportStdio:
		errs = append(errs, c.checkStdio()...)
	default:
		errs = append(errs, fmt.Errorf("transport: unknown transport %q; use http, sse or stdio", c.Transport))
	}

	return errors.Join(errs...)
}

// transport returns the configured transport, or http when none is set.
func (c *Config) transport() Transport {
	if c.Transport == "" {
		return TransportHTTP
	}

	return c.Transport
}

// checkHTTP checks the keys of the http and sse transports.
func (c *Config) checkHTTP() []error {
	var errs []error

	switch u, err := url.Parse(c.URL); {
	case c.URL == "":
		errs = append(errs, fmt.Errorf("url: required with transport %s", c.transport()))
	case err != nil:
		errs = append(errs, fmt.Errorf("url: %w", err))
	case (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("url: %q must be an absolute http or https URL", c.URL))
	}

	if c.Timeout > httpclient.ResponseHeaderTimeout {
		errs = append(errs, fmt.Errorf("timeout: %s is above %s, the longest the HTTP client waits for response headers with transport %s",
			c.Timeout, httpclient.ResponseHeaderTimeout, c.transport()))
	}

	if len(c.Command) > 0 {
		errs = append(errs, fmt.Errorf("command: only for transport stdio, not %s", c.transport()))
	}

	if len(c.Env) > 0 {
		errs = append(errs, fmt.Errorf("env: only for transport stdio, not %s", c.transport()))
	}

	for _, name := range sortedKeys(c.Headers) {
		key := "headers." + name

		switch {
		case !headerNamePattern.MatchString(name):
			errs = append(errs, fmt.Errorf("%s: %q is not a valid header name", key, name))
		case slices.Contains(reservedHeaders, http.CanonicalHeaderKey(name)):
			errs = append(errs, fmt.Errorf("%s: the MCP transport sets this header itself", key))
		}

		errs = append(errs, c.Headers[name].check(key)...)
	}

	return errs
}

// checkStdio checks the keys of the stdio transport.
func (c *Config) checkStdio() []error {
	var errs []error

	if len(c.Command) == 0 || strings.TrimSpace(c.Command[0]) == "" {
		errs = append(errs, errors.New("command: required with transport stdio"))
	}

	if c.URL != "" {
		errs = append(errs, errors.New("url: only for transports http and sse, not stdio"))
	}

	if len(c.Headers) > 0 {
		errs = append(errs, errors.New("headers: only for transports http and sse, not stdio"))
	}

	for _, name := range sortedKeys(c.Env) {
		key := "env." + name
		if !envNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: %q is not a valid environment variable name", key, name))
		}

		errs = append(errs, c.Env[name].check(key)...)
	}

	return errs
}

// check reports a value that is set both ways or neither, or has a prefix
// without a secret.
func (v Value) check(key string) []error {
	switch {
	case v.Secret != nil && v.Value != "":
		return []error{fmt.Errorf("%s: set one of value and secret, not both", key)}
	case v.Secret == nil && v.Value == "":
		return []error{fmt.Errorf("%s: set value or secret", key)}
	case v.Secret == nil && v.Prefix != "":
		return []error{fmt.Errorf("%s.prefix: only with secret; put the whole text in value", key)}
	default:
		return nil
	}
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

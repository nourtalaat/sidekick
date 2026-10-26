package mcp

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// maxMessageSize bounds one message from the server: a JSON response body
// or one server-sent event. A result is cut much smaller later; this only
// stops a server from making Sidekick read without end.
const maxMessageSize = 4 << 20

// eventStreamType is the media type of server-sent events.
const eventStreamType = "text/event-stream"

var (
	// errMessageTooLarge is returned when a response body is larger than
	// maxMessageSize.
	errMessageTooLarge = fmt.Errorf("the server sent a message larger than %d bytes", maxMessageSize)
)

// newTransportFunc returns a function that makes a new transport for each
// connect attempt, as the config's transport says.
func newTransportFunc(cfg Config, deps registry.Deps) func(context.Context) (mcp.Transport, error) {
	if cfg.transport() == TransportStdio {
		return func(ctx context.Context) (mcp.Transport, error) {
			cmd, err := command(ctx, cfg, deps.Secrets)
			if err != nil {
				return nil, err
			}

			return &mcp.CommandTransport{Command: cmd}, nil
		}
	}

	client := httpClient(cfg, deps)

	if cfg.transport() == TransportSSE {
		return func(context.Context) (mcp.Transport, error) {
			return &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: client, MaxEventSize: maxMessageSize}, nil
		}
	}

	return func(context.Context) (mcp.Transport, error) {
		return &mcp.StreamableClientTransport{
			Endpoint:     cfg.URL,
			HTTPClient:   client,
			MaxEventSize: maxMessageSize,
		}, nil
	}
}

// httpClient returns a copy of the shared HTTP client, or of a new one
// from httpclient.New when deps has none, that adds the configured headers
// and limits response bodies. It has no overall timeout, because the
// server-sent event stream stays open for the whole session; each call has
// its own deadline instead.
func httpClient(cfg Config, deps registry.Deps) *http.Client {
	base := deps.HTTP
	if base == nil {
		base = httpclient.New()
	}

	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	client := *base
	client.Timeout = 0
	client.Transport = &headerTransport{next: next, headers: cfg.Headers, secrets: deps.Secrets}

	return &client
}

// headerTransport adds static headers to every request, reading secret
// values on each request so a rotated secret is used without a restart,
// and limits the size of response bodies other than event streams.
type headerTransport struct {
	next    http.RoundTripper
	headers map[string]Value
	secrets registry.Secrets
}

// RoundTrip implements http.RoundTripper.
func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.headers) > 0 {
		req = req.Clone(req.Context())

		for name, v := range t.headers {
			value, err := v.resolve(req.Context(), t.secrets)
			if err != nil {
				return nil, fmt.Errorf("header %s: %w", name, err)
			}

			req.Header.Set(name, value)
		}
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	if mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mediaType != eventStreamType {
		resp.Body = &limitedBody{ReadCloser: resp.Body, left: maxMessageSize}
	}

	return resp, nil
}

// limitedBody fails a read once more than its limit has been read.
type limitedBody struct {
	io.ReadCloser

	left int64
}

// Read implements io.Reader.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errMessageTooLarge
	}

	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}

	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)

	if b.left < 0 {
		return n, errMessageTooLarge
	}

	return n, err //nolint:wrapcheck // io.Reader callers compare err with io.EOF, so it must stay unwrapped.
}

// command builds the child process of a stdio source. It gets only PATH
// and the configured environment, never Sidekick's own environment.
func command(ctx context.Context, cfg Config, s registry.Secrets) (*exec.Cmd, error) {
	env := []string{"PATH=" + os.Getenv("PATH")}

	for _, name := range sortedKeys(cfg.Env) {
		value, err := cfg.Env[name].resolve(ctx, s)
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", name, err)
		}

		env = append(env, name+"="+value)
	}

	// The command has no context: mcp.CommandTransport stops the process
	// when the session closes, first by closing its input, then by signal.
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...) //nolint:gosec,noctx // The operator configures the server; the transport stops it.
	cmd.Env = env

	return cmd, nil
}

// resolve returns the text of a value, reading its secret when it has one.
func (v Value) resolve(ctx context.Context, s registry.Secrets) (string, error) {
	if v.Secret == nil {
		return v.Value, nil
	}

	if s == nil {
		return "", errNoSecrets
	}

	text, err := secrets.ResolveText(ctx, s, *v.Secret)
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}

	return v.Prefix + text, nil
}

// usesSecrets reports whether any header or environment value names a
// secret.
func (c *Config) usesSecrets() bool {
	for _, values := range []map[string]Value{c.Headers, c.Env} {
		for _, v := range values {
			if v.Secret != nil {
				return true
			}
		}
	}

	return false
}

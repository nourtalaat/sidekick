package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxResponseSize bounds one response body. Results are cut much smaller
// later; an answer above it is a tool error that asks for a narrower query.
const maxResponseSize = 16 << 20

// maxErrorBodySize bounds the part of an error answer that is read for the
// server's message.
const maxErrorBodySize = 64 << 10

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("the answer is larger than %d MiB; narrow the query, for example with more label matchers or a shorter range",
		maxResponseSize>>20)
)

// Names of the services in error messages.
const (
	servicePrometheus   = "Prometheus"
	serviceAlertmanager = "Alertmanager"
)

// client sends requests to one API root with the source's credentials.
type client struct {
	http      *http.Client
	root      string
	service   string
	userAgent string
	auth      *httpauth.Auth
}

// statusError is an answer that is not a success.
type statusError struct {
	service string
	code    int
	// errorType is the Prometheus error type, such as bad_data. It may be
	// empty.
	errorType string
	// message is the server's own error message, untrusted.
	message string
}

// Error returns the status and the server's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("%s answered %d", e.service, e.code)
	if e.errorType != "" {
		text += " " + e.errorType
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// get sends a GET request for path, under the client's root, with query,
// and decodes the JSON answer into out. An answer that is not a success
// returns a *statusError.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	target := c.root + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	if err := c.auth.Apply(ctx, req); err != nil {
		return fmt.Errorf("add the credentials: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request to %s: %w", c.service, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return readStatusError(c.service, resp.StatusCode, resp.Body)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode the answer of %s: %w", c.service, err)
	}

	return nil
}

// limitTransport fails the read of any response body larger than
// maxResponseSize, so a huge answer cannot make Sidekick read without end.
type limitTransport struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	return resp, nil
}

// readStatusError returns the *statusError of an answer with code. The
// Prometheus API sends {"errorType": "...", "error": "..."}; Alertmanager
// sends a JSON string or {"message": "..."}; a proxy in front of either
// may send plain text.
func readStatusError(service string, code int, body io.Reader) error {
	e := &statusError{service: service, code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		ErrorType string `json:"errorType"`
		Error     string `json:"error"`
		Message   string `json:"message"`
	}

	var text string

	switch {
	case json.Unmarshal(data, &parsed) == nil:
		e.errorType = source.CutText(parsed.ErrorType, maxNameLen)
		text = parsed.Error
		if text == "" {
			text = parsed.Message
		}
	case json.Unmarshal(data, &text) == nil:
	default:
		text = string(data)
	}

	e.message = source.CutText(strings.TrimSpace(text), maxMessageLen)

	return e
}

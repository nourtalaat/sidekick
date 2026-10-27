package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxResponseSize bounds one response body. The largest answers are pages
// of log entries; results are cut much smaller later.
const maxResponseSize = 16 << 20

// maxErrorBodySize bounds the part of an error answer that is read for
// Google Cloud's message.
const maxErrorBodySize = 64 << 10

// errResponseTooLarge is returned when a response body is larger than
// maxResponseSize.
var errResponseTooLarge = fmt.Errorf("the response from Google Cloud is larger than %d bytes", maxResponseSize)

// client sends requests to the Google Cloud APIs with the source's tokens.
type client struct {
	http      *http.Client
	tokens    oauth2.TokenSource
	userAgent string
}

// statusError is an answer of Google Cloud that is not a success.
type statusError struct {
	code int
	// status is the gRPC status name Google Cloud sends, such as
	// PERMISSION_DENIED. It may be empty.
	status string
	// message is Google Cloud's own error message, untrusted.
	message string
}

// Error returns the status and Google Cloud's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Google Cloud answered %d", e.code)
	if e.status != "" {
		text += " " + e.status
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// post sends body as JSON in a POST request for target, and decodes the
// JSON answer into out. Only APIs that read take a POST from the source,
// such as the log entry search.
func (c *client) post(ctx context.Context, target string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	return c.do(ctx, http.MethodPost, target, data, out)
}

// do sends one request with a token and decodes the JSON answer into out.
// An answer that is not a success returns a *statusError.
func (c *client) do(ctx context.Context, method, target string, body []byte, out any) error {
	token, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("get an access token: %w", err)
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("User-Agent", c.userAgent)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return readStatusError(resp.StatusCode, resp.Body)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w", err)
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

// readStatusError returns the *statusError of an answer with code. Most
// Google Cloud APIs send {"error": {"code", "message", "status"}}; the
// Prometheus API sends {"error": "<message>"} instead.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Error any `json:"error"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return e
	}

	switch v := parsed.Error.(type) {
	case string:
		e.message = source.CutText(v, maxMessageLen)
	case map[string]any:
		message, _ := v["message"].(string)
		status, _ := v["status"].(string)
		e.message = source.CutText(message, maxMessageLen)
		e.status = source.CutText(status, maxNameLen)
	}

	return e
}

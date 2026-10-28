package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxResponseSize bounds one response body. The largest answers are Log
// Analytics query results; results are cut much smaller later.
const maxResponseSize = 16 << 20

// maxErrorBodySize bounds the part of an error answer that is read for
// Azure's message.
const maxErrorBodySize = 64 << 10

// maxInnerErrors bounds how deep readStatusError follows inner errors.
const maxInnerErrors = 8

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("the response from Azure is larger than %d bytes", maxResponseSize)
	// errForeignLink is returned for a next page link that does not point
	// at the API the first page came from, so the token is never sent
	// elsewhere.
	errForeignLink = errors.New("the next page link points at another host")
)

// client sends requests to the Azure APIs with the source's tokens.
type client struct {
	http      *http.Client
	tokens    *tokens
	userAgent string
}

// statusError is an answer of Azure that is not a success.
type statusError struct {
	code int
	// errorCode is Azure's own error code, such as AuthorizationFailed. It
	// may be empty.
	errorCode string
	// message is Azure's own error message, untrusted.
	message string
}

// Error returns the status and Azure's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Azure answered %d", e.code)
	if e.errorCode != "" {
		text += " " + e.errorCode
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// get sends a GET request for target with query and a token for scope,
// and decodes the JSON answer into out.
func (c *client) get(ctx context.Context, scope, target string, query url.Values, out any) error {
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return c.do(ctx, scope, http.MethodGet, target, nil, out)
}

// next reads the page at link, which must start with root, the API the
// first page came from.
func (c *client) next(ctx context.Context, root, link string, out any) error {
	if !strings.HasPrefix(link, root+"/") {
		return errForeignLink
	}

	return c.do(ctx, armScope, http.MethodGet, link, nil, out)
}

// post sends body as JSON in a POST request for target, and decodes the
// JSON answer into out. Only the Log Analytics query, which reads, takes a
// POST from the source.
func (c *client) post(ctx context.Context, scope, target string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	return c.do(ctx, scope, http.MethodPost, target, data, out)
}

// do sends one request with a token for scope and decodes the JSON answer
// into out. An answer that is not a success returns a *statusError.
func (c *client) do(ctx context.Context, scope, method, target string, body []byte, out any) error {
	token, err := c.tokens.token(ctx, scope)
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
	req.Header.Set("Authorization", "Bearer "+token)
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

// azureError is the error object of an Azure answer. Log Analytics nests
// the useful message in inner errors.
type azureError struct {
	Code       string      `json:"code"`
	Message    string      `json:"message"`
	InnerError *azureError `json:"innererror"`
}

// readStatusError returns the *statusError of an answer with code, with
// Azure's error code and its most detailed message.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Error *azureError `json:"error"`
	}

	if json.Unmarshal(data, &parsed) != nil || parsed.Error == nil {
		return e
	}

	e.errorCode = source.CutText(parsed.Error.Code, maxNameLen)
	message := parsed.Error.Message

	inner := parsed.Error.InnerError
	for i := 0; inner != nil && i < maxInnerErrors; i++ {
		if inner.Message != "" {
			message = inner.Message
		}

		inner = inner.InnerError
	}

	e.message = source.CutText(message, maxMessageLen)

	return e
}

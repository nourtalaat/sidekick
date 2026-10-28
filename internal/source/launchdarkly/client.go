package launchdarkly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// apiVersion is the REST API version the source pins with the
// LD-API-Version header, so answers keep their shape whatever version the
// token was made with.
const apiVersion = "20240415"

// maxResponseSize bounds one response body. The largest answers are flags
// with many rules; results are cut much smaller later.
const maxResponseSize = 8 << 20

// maxErrorBodySize bounds the part of an error answer that is read for
// LaunchDarkly's message.
const maxErrorBodySize = 64 << 10

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("the response from LaunchDarkly is larger than %d bytes", maxResponseSize)
	// errEmptyToken is returned when the token secret holds nothing.
	errEmptyToken = errors.New("the token secret is empty")
)

// statusError is an answer of LaunchDarkly that is not a success.
type statusError struct {
	code int
	// errorCode is LaunchDarkly's error class, such as invalid_request. It
	// may be empty.
	errorCode string
	// message is LaunchDarkly's own error message, untrusted.
	message string
}

// Error returns the status and LaunchDarkly's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("LaunchDarkly answered %d", e.code)
	if e.errorCode != "" {
		text += " " + e.errorCode
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the REST API with the source's token.
type client struct {
	http      *http.Client
	baseURL   string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
}

// get sends a GET request for path, under the base URL, with query, and
// decodes the JSON answer into out. An answer that is not a success
// returns a *statusError.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	token, err := c.readToken(ctx)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", token)
	req.Header.Set("LD-API-Version", apiVersion)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body := httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return readStatusError(resp.StatusCode, body)
	}

	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w", err)
	}

	return nil
}

// readToken returns the token from its secret, read on every request so a
// rotated token needs no restart.
func (c *client) readToken(ctx context.Context) (string, error) {
	raw, err := c.secrets.Resolve(ctx, c.token)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", c.token.Name, c.token.Key, err)
	}

	token := string(bytes.TrimSpace(raw))
	if token == "" {
		return "", errEmptyToken
	}

	return token, nil
}

// readStatusError returns the *statusError of an answer with code, with
// LaunchDarkly's message when the body holds one.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	if json.Unmarshal(data, &parsed) == nil {
		e.errorCode = source.CutText(parsed.Code, flags.MaxNameLen)
		e.message = source.CutText(parsed.Message, flags.MaxTextLen)
	}

	return e
}

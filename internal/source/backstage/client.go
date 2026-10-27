package backstage

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
)

// maxResponseSize bounds one response body. The largest answers are pages
// of the entity list that ServiceDependencies reads.
const maxResponseSize = 16 << 20

// maxErrorBodySize bounds the part of an error answer that is read for
// Backstage's message.
const maxErrorBodySize = 64 << 10

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("Backstage sent a response larger than %d bytes", maxResponseSize)
	// errEmptyToken is returned when the token secret holds nothing.
	errEmptyToken = errors.New("the token secret is empty")
)

// statusError is an answer of Backstage that is not a success.
type statusError struct {
	code int
	// message is Backstage's own error message, untrusted.
	message string
}

// Error returns the status and Backstage's message.
func (e *statusError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("Backstage answered %d", e.code)
	}

	return fmt.Sprintf("Backstage answered %d: %s", e.code, e.message)
}

// errorBody is the body of a Backstage error answer.
type errorBody struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// client sends requests to the catalog API with the source's token.
type client struct {
	http      *http.Client
	apiURL    string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
}

// get sends a GET request for path, under the catalog API, with query, and
// decodes the JSON answer into out. An answer that is not a success
// returns a *statusError.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	token, err := c.readToken(ctx)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}

	target := c.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
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
// Backstage's message when the body holds one.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed errorBody
	if json.Unmarshal(data, &parsed) == nil {
		e.message = source.CutText(parsed.Error.Message, maxTitleLen)
	}

	return e
}

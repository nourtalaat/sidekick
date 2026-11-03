package datadogapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The limits on what the client reads from Datadog.
const (
	// maxResponseBytes is the largest answer read. A monitor definition is
	// a few kilobytes.
	maxResponseBytes = 2 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of Datadog that is kept.
	maxMessageBytes = 400
)

// The headers Datadog reads its two keys from.
const (
	apiHeader = "DD-API-KEY"
	appHeader = "DD-APPLICATION-KEY"
)

var (
	errTooLarge = errors.New("the answer from Datadog is larger than the action reads")
	errEmptyKey = errors.New("a key secret is empty")
	errNoBody   = errors.New("the answer from Datadog is not something this action can read")
)

// StatusError is an answer of Datadog that is not a success.
type StatusError struct {
	Code int
	// Message is Datadog's own message, untrusted, cut.
	Message string
}

// Error implements error.
func (e *StatusError) Error() string {
	text := fmt.Sprintf("Datadog answered %d", e.Code)
	if e.Message != "" {
		text += ": " + e.Message
	}

	return text
}

// Client sends requests to the Datadog API with the action's keys.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  registry.SecretRef
	appKey  registry.SecretRef
	secrets registry.Secrets
}

// NewClient makes a client that sends its requests with hc to baseURL, and
// reads its keys from secrets. It makes no request.
func NewClient(hc *http.Client, baseURL string, apiKey, appKey registry.SecretRef, secrets registry.Secrets) *Client {
	return &Client{http: hc, baseURL: baseURL, apiKey: apiKey, appKey: appKey, secrets: secrets}
}

// Do sends one request and decodes the JSON answer into out, if out is set.
// path starts with a slash and may hold a query. An answer that is not a
// success returns a *StatusError, wrapped with action.ErrPermanent when
// sending it again cannot help: 400, 401, 403, 404, 409 and 422. A 429 or a
// server error is not, so Sidekick tries again.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	apiKey, appKey, err := c.keys(ctx)
	if err != nil {
		return fmt.Errorf("read the keys: %w", err)
	}

	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the body: %w", err)
		}

		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set(apiHeader, apiKey)
	req.Header.Set(appHeader, appKey)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	limited := httpclient.LimitBody(resp.Body, maxResponseBytes, errTooLarge)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return statusFailure(resp.StatusCode, limited)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w: %w", errNoBody, err)
	}

	return nil
}

// keys reads both keys. They are read on every request, so rotated keys are
// used without a restart.
func (c *Client) keys(ctx context.Context) (apiKey, appKey string, err error) {
	apiKey, err = secrets.ResolveText(ctx, c.secrets, c.apiKey)
	if err != nil {
		return "", "", fmt.Errorf("resolve secret %s/%s: %w", c.apiKey.Name, c.apiKey.Key, err)
	}

	appKey, err = secrets.ResolveText(ctx, c.secrets, c.appKey)
	if err != nil {
		return "", "", fmt.Errorf("resolve secret %s/%s: %w", c.appKey.Name, c.appKey.Key, err)
	}

	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(appKey) == "" {
		return "", "", errEmptyKey
	}

	return strings.TrimSpace(apiKey), strings.TrimSpace(appKey), nil
}

// statusFailure makes the error of an answer that is not a success.
func statusFailure(code int, body io.Reader) error {
	e := &StatusError{Code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err == nil {
		e.Message = source.CutText(messageOf(data), maxMessageBytes)
	}

	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", action.ErrPermanent, e)
	default:
		return e
	}
}

// messageOf reads the messages of an error answer, which Datadog writes as
// {"errors": ["..."]}. Anything else gives no message.
func messageOf(data []byte) string {
	var parsed struct {
		Errors []string `json:"errors"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}

	return strings.Join(parsed.Errors, "; ")
}

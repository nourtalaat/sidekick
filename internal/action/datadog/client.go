package datadog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
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

// statusError is an answer of Datadog that is not a success.
type statusError struct {
	code int
	// message is Datadog's own message, untrusted, cut.
	message string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Datadog answered %d", e.code)
	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the monitors API with the action's keys.
type client struct {
	http    *http.Client
	baseURL string
	apiKey  registry.SecretRef
	appKey  registry.SecretRef
	secrets registry.Secrets
}

// get reads the monitor with id as the fields Datadog sent.
func (c *client) get(ctx context.Context, id int64) (map[string]json.RawMessage, error) {
	var live map[string]json.RawMessage
	if err := c.do(ctx, http.MethodGet, monitorPath(id), nil, &live); err != nil {
		return nil, fmt.Errorf("read monitor %d: %w", id, err)
	}

	return live, nil
}

// validate asks Datadog to check the definition of the monitor with id.
// A 400 answer is Datadog finding the definition invalid, and the error
// says why, wrapping action.ErrLimit.
//
// ASSUMPTION: POST /api/v1/monitor/{id}/validate takes the monitor
// definition as its body, answers 2xx when it is valid, and answers 400
// with {"errors": ["..."]} when it is not.
func (c *client) validate(ctx context.Context, id int64, definition json.RawMessage) error {
	err := c.do(ctx, http.MethodPost, monitorPath(id)+"/validate", definition, nil)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusBadRequest {
		return fmt.Errorf("the new definition is not accepted by Datadog: %s: %w", status.message, action.ErrLimit)
	}

	if err != nil {
		return fmt.Errorf("validate monitor %d: %w", id, err)
	}

	return nil
}

// update writes the fields of body to the monitor with id.
func (c *client) update(ctx context.Context, id int64, body map[string]json.RawMessage) error {
	if err := c.do(ctx, http.MethodPut, monitorPath(id), body, nil); err != nil {
		return fmt.Errorf("update monitor %d: %w", id, err)
	}

	return nil
}

// remove deletes the monitor with id. A monitor that is gone already is not
// an error.
func (c *client) remove(ctx context.Context, id int64) error {
	err := c.do(ctx, http.MethodDelete, monitorPath(id), nil, nil)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil
	}

	if err != nil {
		return fmt.Errorf("delete monitor %d: %w", id, err)
	}

	return nil
}

// monitorPath is the path of the monitor with id.
func monitorPath(id int64) string {
	return "/api/v1/monitor/" + strconv.FormatInt(id, 10)
}

// do sends one request and decodes the JSON answer into out, if out is set.
// An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help: 400, 401, 403,
// 404, 409 and 422. A 429 or a server error is not, so Sidekick tries again.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
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
func (c *client) keys(ctx context.Context) (apiKey, appKey string, err error) {
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
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err == nil {
		e.message = source.CutText(messageOf(data), maxMessageBytes)
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

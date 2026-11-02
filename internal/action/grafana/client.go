package grafana

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

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The limits on what the client reads from Grafana.
const (
	// maxResponseBytes is the largest answer read. A rule is a few kilobytes.
	maxResponseBytes = 2 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of Grafana that is kept.
	maxMessageBytes = 400
)

// disableProvenanceHeader is the header that stops Grafana from marking a
// rule it writes as provisioned by the API, which would lock the rule in the
// Grafana UI. Grafana looks only at whether the header is there
// (pkg/services/ngalert/api/api_provisioning.go, determineManagerProperties,
// tag v13.2.5; the same check in v12.4.14 and v11.6.9). Every write and every
// delete carries it.
const (
	disableProvenanceHeader = "X-Disable-Provenance"
	disableProvenanceValue  = "true"
)

// rulesPath is the root of the alerting provisioning API for rules.
const rulesPath = "/api/v1/provisioning/alert-rules/"

var (
	errTooLarge   = errors.New("the answer from Grafana is larger than the action reads")
	errEmptyToken = errors.New("the token secret is empty")
	errNoBody     = errors.New("the answer from Grafana is not something this action can read")
)

// statusError is an answer of Grafana that is not a success.
type statusError struct {
	code int
	// message is Grafana's own message, untrusted, cut.
	message string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Grafana answered %d", e.code)
	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the alert rule provisioning API with the action's
// token.
type client struct {
	http    *http.Client
	baseURL string
	token   registry.SecretRef
	secrets registry.Secrets
}

// get reads the rule with uid as the fields Grafana sent.
func (c *client) get(ctx context.Context, uid string) (map[string]json.RawMessage, error) {
	var live map[string]json.RawMessage
	if err := c.do(ctx, http.MethodGet, rulePath(uid), nil, &live); err != nil {
		return nil, fmt.Errorf("read rule %s: %w", uid, err)
	}

	return live, nil
}

// update writes the whole rule in body to the rule with uid. Grafana has no
// call that only checks a rule, so a rule it refuses is refused here, and
// nothing is stored.
func (c *client) update(ctx context.Context, uid string, body json.RawMessage) error {
	if err := c.do(ctx, http.MethodPut, rulePath(uid), body, nil); err != nil {
		return fmt.Errorf("update rule %s: %w", uid, err)
	}

	return nil
}

// remove deletes the rule with uid. A rule that is gone already is not an
// error: Grafana answers 204 for it.
func (c *client) remove(ctx context.Context, uid string) error {
	err := c.do(ctx, http.MethodDelete, rulePath(uid), nil, nil)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil
	}

	if err != nil {
		return fmt.Errorf("delete rule %s: %w", uid, err)
	}

	return nil
}

// do sends one request and decodes the JSON answer into out, if out is set.
// A write or a delete carries the header that keeps the rule editable in the
// UI. An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help: 400, 401, 403, 404,
// 409 and 422. A 429 or a server error is not, so Sidekick tries again.
func (c *client) do(ctx context.Context, method, path string, body json.RawMessage, out any) error {
	token, err := c.bearer(ctx)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if method != http.MethodGet {
		req.Header.Set(disableProvenanceHeader, disableProvenanceValue)
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

// bearer reads the token. It is read on every request, so a rotated token is
// used without a restart.
func (c *client) bearer(ctx context.Context) (string, error) {
	token, err := secrets.ResolveText(ctx, c.secrets, c.token)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", c.token.Name, c.token.Key, err)
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", errEmptyToken
	}

	return token, nil
}

// rulePath is the path of the rule with uid.
func rulePath(uid string) string {
	return rulesPath + url.PathEscape(uid)
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

// messageOf reads the message of an error answer, which Grafana writes as
// {"message": "..."}. Anything else gives no message.
func messageOf(data []byte) string {
	var parsed struct {
		Message string `json:"message"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}

	return parsed.Message
}

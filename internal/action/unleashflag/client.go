package unleashflag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The limits on what the client reads from Unleash.
const (
	// maxResponseBytes is the largest answer read. The answers are one
	// flag, the strategies of one environment and one event.
	maxResponseBytes = 4 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of Unleash that is kept.
	maxMessageBytes = 400
)

// The paths of the Admin API, under the base URL.
const (
	projectsPath = "/api/admin/projects/"
	eventsPath   = "/api/admin/search/events"
)

// skipChangeRequest is the permission that lets a caller change an
// environment that uses change requests directly. Unleash names it in the
// 403 it sends to a caller that lacks it.
const skipChangeRequest = "SKIP_CHANGE_REQUEST"

var (
	errTooLarge       = errors.New("the answer from Unleash is larger than the action reads")
	errEmptyToken     = errors.New("the token secret is empty")
	errChangeRequests = errors.New("this environment uses change requests, and Unleash refuses direct changes to it; " +
		"make the change in Unleash with a change request")
)

// statusError is an answer of Unleash that is not a success.
type statusError struct {
	code int
	// name is Unleash's error class, such as PermissionError.
	name string
	// message is Unleash's own message, untrusted, cut.
	message string
	// permissions are the permissions a 403 says the caller lacks.
	permissions []string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Unleash answered %d", e.code)
	if e.name != "" {
		text += " " + e.name
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the Admin API with the action's token.
type client struct {
	http      *http.Client
	baseURL   string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
}

// featureView is the part of a flag the action reads.
type featureView struct {
	Environments []featureEnvironment `json:"environments"`
}

// featureEnvironment is a flag's state in one environment.
type featureEnvironment struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// event is the part of an Unleash event the action reads. Data and PreData
// are the strategy an event of a strategy holds after and before the
// change.
type event struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	GroupID string          `json:"groupId"`
	Data    json.RawMessage `json:"data"`
	PreData json.RawMessage `json:"preData"`
}

// feature reads the flag. A flag that does not exist in the project, or is
// archived, answers 404.
func (c *client) feature(ctx context.Context, project, flag string) (featureView, error) {
	var view featureView
	if err := c.do(ctx, http.MethodGet, featurePath(project, flag), nil, nil, &view); err != nil {
		return featureView{}, fmt.Errorf("read flag %s of project %s: %w", flag, project, err)
	}

	return view, nil
}

// strategies reads the strategies of the flag in one environment.
func (c *client) strategies(ctx context.Context, project, flag, environment string) ([]liveStrategy, error) {
	var list []liveStrategy
	if err := c.do(ctx, http.MethodGet, environmentPath(project, flag, environment)+"/strategies", nil, nil, &list); err != nil {
		return nil, fmt.Errorf("read the strategies of flag %s in %s: %w", flag, environment, err)
	}

	return list, nil
}

// newestEvent reads the newest event of the flag in one environment, or the
// zero event, which has ID 0, when there is none. Unleash sorts events
// newest first, and numbers them from 1.
func (c *client) newestEvent(ctx context.Context, project, flag, environment string) (event, error) {
	query := url.Values{
		"project":     {"IS:" + project},
		"environment": {"IS:" + environment},
		"feature":     {"IS:" + flag},
		"limit":       {"1"},
	}

	var page struct {
		Events []event `json:"events"`
	}

	if err := c.do(ctx, http.MethodGet, eventsPath, query, nil, &page); err != nil {
		return event{}, fmt.Errorf("read the newest event of flag %s in %s: %w", flag, environment, err)
	}

	if len(page.Events) == 0 {
		return event{}, nil
	}

	return page.Events[0], nil
}

// toggle turns the flag on or off in one environment. Turning off an
// environment that is off changes nothing.
func (c *client) toggle(ctx context.Context, project, flag, environment string, on bool) error {
	verb := "off"
	if on {
		verb = "on"
	}

	if err := c.do(ctx, http.MethodPost, environmentPath(project, flag, environment)+"/"+verb, nil, nil, nil); err != nil {
		return fmt.Errorf("turn flag %s %s in %s: %w", flag, verb, environment, err)
	}

	return nil
}

// createStrategy adds a strategy to the flag in one environment. Unleash
// gives it a new ID.
func (c *client) createStrategy(ctx context.Context, project, flag, environment string, s strategy) error {
	if err := c.do(ctx, http.MethodPost, environmentPath(project, flag, environment)+"/strategies", nil, s, nil); err != nil {
		return fmt.Errorf("add a strategy to flag %s in %s: %w", flag, environment, err)
	}

	return nil
}

// updateStrategy writes the whole strategy with id.
func (c *client) updateStrategy(ctx context.Context, project, flag, environment, id string, s strategy) error {
	if err := c.do(ctx, http.MethodPut, strategyPath(project, flag, environment, id), nil, s, nil); err != nil {
		return fmt.Errorf("update strategy %s of flag %s in %s: %w", id, flag, environment, err)
	}

	return nil
}

// deleteStrategy removes the strategy with id.
func (c *client) deleteStrategy(ctx context.Context, project, flag, environment, id string) error {
	if err := c.do(ctx, http.MethodDelete, strategyPath(project, flag, environment, id), nil, nil, nil); err != nil {
		return fmt.Errorf("delete strategy %s of flag %s in %s: %w", id, flag, environment, err)
	}

	return nil
}

// featurePath is the path of a flag. Every key has been checked against
// keyPattern, and is escaped as well.
func featurePath(project, flag string) string {
	return projectsPath + url.PathEscape(project) + "/features/" + url.PathEscape(flag)
}

// environmentPath is the path of a flag in one environment.
func environmentPath(project, flag, environment string) string {
	return featurePath(project, flag) + "/environments/" + url.PathEscape(environment)
}

// strategyPath is the path of one strategy of a flag in one environment.
func strategyPath(project, flag, environment, id string) string {
	return environmentPath(project, flag, environment) + "/strategies/" + url.PathEscape(id)
}

// do sends one request and decodes the JSON answer into out, if out is set.
// An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help: 400, 401, 403,
// 404, 409 and 422. A 429 or a server error is not, so Sidekick tries
// again.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	token, err := c.readToken(ctx)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}

	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the body: %w", err)
		}

		reader = bytes.NewReader(raw)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", token)
	req.Header.Set("User-Agent", c.userAgent)

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

// statusFailure makes the error of an answer that is not a success. A 403
// that names SKIP_CHANGE_REQUEST is Unleash refusing a direct change in an
// environment that uses change requests.
func statusFailure(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err == nil {
		var parsed struct {
			Name        string   `json:"name"`
			Message     string   `json:"message"`
			Permissions []string `json:"permissions"`
		}

		if json.Unmarshal(data, &parsed) == nil {
			e.name = source.CutText(parsed.Name, maxMessageBytes)
			e.message = source.CutText(parsed.Message, maxMessageBytes)
			e.permissions = parsed.Permissions
		}
	}

	switch {
	case code == http.StatusForbidden && slices.Contains(e.permissions, skipChangeRequest):
		return fmt.Errorf("%w: %w", action.ErrPermanent, errChangeRequests)
	case code == http.StatusBadRequest, code == http.StatusUnauthorized, code == http.StatusForbidden,
		code == http.StatusNotFound, code == http.StatusConflict, code == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", action.ErrPermanent, e)
	default:
		return e
	}
}

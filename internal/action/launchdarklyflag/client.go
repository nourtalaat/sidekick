package launchdarklyflag

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

// defaultBaseURL is the root of LaunchDarkly's commercial instance. The EU
// instance is https://app.eu.launchdarkly.com and the Federal one is
// https://app.launchdarkly.us.
const defaultBaseURL = "https://app.launchdarkly.com"

// apiVersion is the REST API version the action pins with the
// LD-API-Version header, so answers keep their shape whatever version the
// token was made with.
const apiVersion = "20240415"

// semanticPatchType is the content type that makes the flag PATCH read its
// body as a semantic patch instead of a JSON patch.
const semanticPatchType = "application/json; domain-model=launchdarkly.semanticpatch"

// The limits on what the client reads from LaunchDarkly.
const (
	// maxResponseBytes is the largest answer read. A flag with many rules
	// is the largest.
	maxResponseBytes = 8 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of LaunchDarkly that is kept.
	maxMessageBytes = 400
)

var (
	errTooLarge   = errors.New("the answer from LaunchDarkly is larger than the action reads")
	errEmptyToken = errors.New("the token secret is empty")
	errNoBody     = errors.New("the answer from LaunchDarkly is not something this action can read")
)

// statusError is an answer of LaunchDarkly that is not a success.
type statusError struct {
	code int
	// errorCode is LaunchDarkly's error class, such as invalid_request. It
	// may be empty.
	errorCode string
	// message is LaunchDarkly's own message, untrusted, cut.
	message string
}

// Error implements error.
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

// flagDoc is the part of a feature flag the action reads, for one
// environment.
type flagDoc struct {
	Key          string               `json:"key"`
	Archived     bool                 `json:"archived"`
	Tags         []string             `json:"tags"`
	Environments map[string]flagState `json:"environments"`
}

// flagState is a flag's configuration in one environment.
type flagState struct {
	On       bool `json:"on"`
	Archived bool `json:"archived"`
	// Version is the environment's version, which changes on every edit
	// of the flag in that environment.
	Version int `json:"version"`
	Site    struct {
		Href string `json:"href"`
	} `json:"_site"`
}

// environmentDoc is the part of an environment the action reads.
type environmentDoc struct {
	ApprovalSettings struct {
		Required     bool     `json:"required"`
		RequiredTags []string `json:"requiredApprovalTags"`
	} `json:"approvalSettings"`
}

// client sends requests to the REST API with the action's token.
type client struct {
	http    *http.Client
	baseURL string
	token   registry.SecretRef
	secrets registry.Secrets
}

// flag reads the flag key of project, with its configuration in env.
func (c *client) flag(ctx context.Context, project, key, env string) (flagDoc, error) {
	var doc flagDoc

	path := "/api/v2/flags/" + url.PathEscape(project) + "/" + url.PathEscape(key)
	if err := c.do(ctx, http.MethodGet, path, url.Values{"env": {env}}, "", nil, &doc); err != nil {
		return flagDoc{}, fmt.Errorf("read flag %s of project %s: %w", key, project, err)
	}

	return doc, nil
}

// environment reads the environment env of project.
func (c *client) environment(ctx context.Context, project, env string) (environmentDoc, error) {
	var doc environmentDoc

	path := "/api/v2/projects/" + url.PathEscape(project) + "/environments/" + url.PathEscape(env)
	if err := c.do(ctx, http.MethodGet, path, nil, "", nil, &doc); err != nil {
		return environmentDoc{}, fmt.Errorf("read environment %s of project %s: %w", env, project, err)
	}

	return doc, nil
}

// scheduledChanges returns how many scheduled changes the flag has in env.
func (c *client) scheduledChanges(ctx context.Context, project, key, env string) (int, error) {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}

	if err := c.do(ctx, http.MethodGet, flagEnvPath(project, key, env)+"/scheduled-changes", nil, "", nil, &list); err != nil {
		return 0, fmt.Errorf("list the scheduled changes of flag %s: %w", key, err)
	}

	return len(list.Items), nil
}

// activeWorkflows returns how many workflows of the flag in env are active.
func (c *client) activeWorkflows(ctx context.Context, project, key, env string) (int, error) {
	var list struct {
		TotalCount int `json:"totalCount"`
	}

	query := url.Values{"status": {"active"}, "limit": {"1"}}
	if err := c.do(ctx, http.MethodGet, flagEnvPath(project, key, env)+"/workflows", query, "", nil, &list); err != nil {
		return 0, fmt.Errorf("list the active workflows of flag %s: %w", key, err)
	}

	return list.TotalCount, nil
}

// turn sends the semantic patch that turns the flag on or off in env, with
// comment, which LaunchDarkly keeps in its audit log. A flag that is
// already in the state asked for is left unchanged.
func (c *client) turn(ctx context.Context, project, key, env string, on bool, comment string) error {
	kind := "turnFlagOff"
	if on {
		kind = "turnFlagOn"
	}

	body := map[string]any{
		"comment":        comment,
		"environmentKey": env,
		"instructions":   []map[string]string{{"kind": kind}},
	}

	path := "/api/v2/flags/" + url.PathEscape(project) + "/" + url.PathEscape(key)

	err := c.do(ctx, http.MethodPatch, path, nil, semanticPatchType, body, nil)

	var status *statusError

	switch {
	case errors.As(err, &status) && status.code == http.StatusMethodNotAllowed:
		return fmt.Errorf("environment %s requires approvals for this flag, so LaunchDarkly refuses a direct change: %w", env, err)
	case errors.As(err, &status) && status.code == http.StatusBadRequest:
		return fmt.Errorf("LaunchDarkly refused the change; it also does so when the change would make a scheduled change or an approval request fail: %w", err)
	case err != nil:
		return fmt.Errorf("turn flag %s of project %s: %w", key, project, err)
	default:
		return nil
	}
}

// flagEnvPath is the path of the flag key of project in env, under the
// projects API.
func flagEnvPath(project, key, env string) string {
	return "/api/v2/projects/" + url.PathEscape(project) + "/flags/" + url.PathEscape(key) + "/environments/" + url.PathEscape(env)
}

// do sends one request and decodes the JSON answer into out, if out is set.
// An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help (400, 401, 403,
// 404 and 405) and with action.ErrConflict for a 409. A 429 or a server
// error is not wrapped, so Sidekick tries again.
func (c *client) do(ctx context.Context, method, path string, query url.Values, contentType string, body, out any) error {
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
	req.Header.Set("LD-API-Version", apiVersion)

	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}

		req.Header.Set("Content-Type", contentType)
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

// readToken returns the token from its secret, read on every request so a
// rotated token needs no restart.
func (c *client) readToken(ctx context.Context) (string, error) {
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

// statusFailure makes the error of an answer that is not a success.
func statusFailure(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBytes))
	if err == nil {
		var parsed struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}

		if json.Unmarshal(data, &parsed) == nil {
			e.errorCode = source.CutText(parsed.Code, maxMessageBytes)
			e.message = source.CutText(parsed.Message, maxMessageBytes)
		}
	}

	switch code {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed:
		return fmt.Errorf("%w: %w", action.ErrPermanent, e)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", action.ErrConflict, e)
	default:
		return e
	}
}

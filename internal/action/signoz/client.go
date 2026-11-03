package signoz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The limits on what the client reads from SigNoz.
const (
	// maxResponseBytes is the largest answer read. A rule is a few
	// kilobytes.
	maxResponseBytes = 2 << 20
	// maxErrorBytes is how much of an error answer is read for its message.
	maxErrorBytes = 64 << 10
	// maxMessageBytes is the longest message of SigNoz that is kept.
	maxMessageBytes = 400
)

// minVersion is the oldest SigNoz release the action supports. From this tag
// to v0.148.1, the files the action relies on differ only by additions and by
// changes that leave the JSON as it was; see the package comment.
var minVersion = release{major: 0, minor: 134, patch: 0}

// releaseOf is the shape of a SigNoz release: v0.148.1, also with a suffix
// such as -cloud.1.
var releaseOf = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

var (
	errTooLarge       = errors.New("the answer from SigNoz is larger than the action reads")
	errEmptyKey       = errors.New("the key secret is empty")
	errNoBody         = errors.New("the answer from SigNoz is not something this action can read")
	errTooOld   error = refusal("this SigNoz is older than the action supports")
)

// refusal is an error for a SigNoz the action will not work with. It matches
// action.ErrLimit, so the model reads it as a refusal with its reason and not
// as a failure to read the rule.
type refusal string

// Error implements error.
func (r refusal) Error() string {
	return string(r)
}

// Is makes the refusal match action.ErrLimit.
func (refusal) Is(target error) bool {
	return target == action.ErrLimit
}

// release is a SigNoz version number.
type release struct {
	major, minor, patch int
}

// statusError is an answer of SigNoz that is not a success.
type statusError struct {
	code int
	// message is SigNoz's own message, untrusted, cut.
	message string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("SigNoz answered %d", e.code)
	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the rules API with the action's key.
type client struct {
	http    *http.Client
	baseURL string
	header  string
	apiKey  registry.SecretRef
	secrets registry.Secrets
	logger  *slog.Logger

	// mu guards checked, which is true once the release of SigNoz was found
	// to be one the action supports, or one it cannot tell.
	mu      sync.Mutex
	checked bool
}

// get reads the rule with id as the keys SigNoz sent.
func (c *client) get(ctx context.Context, id string) (map[string]json.RawMessage, error) {
	var answer struct {
		Status string                     `json:"status"`
		Data   map[string]json.RawMessage `json:"data"`
	}

	if err := c.do(ctx, http.MethodGet, rulePath(id), nil, &answer); err != nil {
		return nil, fmt.Errorf("read rule %s: %w", id, err)
	}

	if answer.Status != "success" || answer.Data == nil {
		return nil, fmt.Errorf("read rule %s: %w: %w", id, action.ErrPermanent, errNoBody)
	}

	return answer.Data, nil
}

// update replaces the rule with id by the rule in body.
func (c *client) update(ctx context.Context, id string, body json.RawMessage) error {
	if err := c.do(ctx, http.MethodPut, rulePath(id), body, nil); err != nil {
		return fmt.Errorf("update rule %s: %w", id, err)
	}

	return nil
}

// remove deletes the rule with id. A rule that is gone already is not an
// error.
func (c *client) remove(ctx context.Context, id string) error {
	err := c.do(ctx, http.MethodDelete, rulePath(id), nil, nil)

	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil
	}

	if err != nil {
		return fmt.Errorf("delete rule %s: %w", id, err)
	}

	return nil
}

// ensureSupported checks, once, that SigNoz is a release the action
// supports. The release comes from GET /api/v1/version. A release below
// minVersion is refused. A version text that is not a release, such as that
// of a custom build, cannot be checked: it is allowed, with one warning, and
// the read back after each write is what protects the rule then.
func (c *client) ensureSupported(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.checked {
		return nil
	}

	var info struct {
		Version string `json:"version"`
	}

	if err := c.do(ctx, http.MethodGet, "/api/v1/version", nil, &info); err != nil {
		return fmt.Errorf("read the SigNoz version: %w", err)
	}

	found, ok := parseRelease(info.Version)

	switch {
	case !ok:
		c.logger.WarnContext(ctx, "the SigNoz version is not a release number, so the action cannot check that it supports it; every change is read back after it is written",
			slog.String("version", source.CutText(info.Version, maxMessageBytes)), slog.String("minimum", minVersion.String()))
	case found.before(minVersion):
		return fmt.Errorf("SigNoz %s is older than %s, the oldest release the action supports; upgrade SigNoz: %w: %w",
			found, minVersion, action.ErrPermanent, errTooOld)
	}

	c.checked = true

	return nil
}

// rulePath is the path of the rule with id.
func rulePath(id string) string {
	return "/api/v2/rules/" + id
}

// do sends one request and decodes the JSON answer into out, if out is set.
// An answer that is not a success returns a *statusError, wrapped with
// action.ErrPermanent when sending it again cannot help: 400, 401, 403, 404,
// 409 and 422. A 429 or a server error is not, so Sidekick tries again.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	key, err := c.key(ctx)
	if err != nil {
		return fmt.Errorf("read the key: %w", err)
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
	req.Header.Set(c.header, key)

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

// key reads the key. It is read on every request, so a rotated key is used
// without a restart.
func (c *client) key(ctx context.Context) (string, error) {
	key, err := secrets.ResolveText(ctx, c.secrets, c.apiKey)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", c.apiKey.Name, c.apiKey.Key, err)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", errEmptyKey
	}

	return key, nil
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

// messageOf reads the messages of an error answer, which SigNoz writes as
// {"status":"error","error":{"message":"...","errors":[{"message":"..."}]}}.
// Anything else gives no message.
func messageOf(data []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}

	parts := []string{}
	if parsed.Error.Message != "" {
		parts = append(parts, parsed.Error.Message)
	}

	for _, e := range parsed.Error.Errors {
		if e.Message != "" {
			parts = append(parts, e.Message)
		}
	}

	return strings.Join(parts, "; ")
}

// parseRelease reads a SigNoz version such as v0.148.1 or
// v0.134.0-cloud.1.
func parseRelease(text string) (release, bool) {
	m := releaseOf.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return release{}, false
	}

	var parts [3]int

	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return release{}, false
		}

		parts[i] = n
	}

	return release{major: parts[0], minor: parts[1], patch: parts[2]}, true
}

// before reports whether r is an older release than other.
func (r release) before(other release) bool {
	switch {
	case r.major != other.major:
		return r.major < other.major
	case r.minor != other.minor:
		return r.minor < other.minor
	default:
		return r.patch < other.patch
	}
}

// String writes the release as SigNoz does.
func (r release) String() string {
	return fmt.Sprintf("v%d.%d.%d", r.major, r.minor, r.patch)
}

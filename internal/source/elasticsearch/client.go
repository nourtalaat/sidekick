package elasticsearch

import (
	"bytes"
	"context"
	"encoding/base64"
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
// of hits and field lists; results are cut much smaller later.
const maxResponseSize = 16 << 20

// maxErrorBodySize bounds the part of an error answer that is read for the
// server's message.
const maxErrorBodySize = 64 << 10

var (
	// errResponseTooLarge is returned when a response body is larger than
	// maxResponseSize.
	errResponseTooLarge = fmt.Errorf("the response is larger than %d bytes", maxResponseSize)
	// errEmptySecret is returned when the API key or password secret holds
	// nothing.
	errEmptySecret = errors.New("the secret is empty")
)

// client sends read requests to the cluster with the source's credentials.
type client struct {
	http      *http.Client
	apiURL    string
	product   string
	userAgent string
	auth      auth
}

// auth says how the client signs in: with an API key, or with a user name
// and password. The secrets are read for each request, so a rotated key or
// password needs no restart.
type auth struct {
	secrets  registry.Secrets
	apiKey   *registry.SecretRef
	username string
	password registry.SecretRef
}

// statusError is an answer of the cluster that is not a success.
type statusError struct {
	product string
	code    int
	// kind is the error type the server sends, such as
	// index_not_found_exception. It may be empty.
	kind string
	// message is the server's own error message, untrusted.
	message string
}

// Error returns the status and the server's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("%s answered %d", e.product, e.code)
	if e.kind != "" {
		text += " " + e.kind
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// errorCause is one error in an error answer.
type errorCause struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// search sends body to the _search API of index and decodes the answer
// into out.
func (c *client) search(ctx context.Context, index string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	return c.do(ctx, http.MethodPost, "/"+index+"/_search", nil, data, out)
}

// fieldCaps reads the _field_caps API of index for the fields that match
// pattern, and decodes the answer into out.
func (c *client) fieldCaps(ctx context.Context, index, pattern string, out any) error {
	return c.do(ctx, http.MethodGet, "/"+index+"/_field_caps", url.Values{"fields": {pattern}}, nil, out)
}

// do sends one request with the credentials and decodes the JSON answer
// into out. The path holds only names the input checks have passed. An
// answer that is not a success returns a *statusError.
func (c *client) do(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	header, err := c.auth.header(ctx)
	if err != nil {
		return fmt.Errorf("read the credentials: %w", err)
	}

	target := c.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
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
	req.Header.Set("Authorization", header)
	req.Header.Set("User-Agent", c.userAgent)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	limited := httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return c.readStatusError(resp.StatusCode, limited)
	}

	dec := json.NewDecoder(limited)
	dec.UseNumber()

	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w", err)
	}

	return nil
}

// readStatusError returns the *statusError of an answer with code. Both
// flavors send {"error": {"type", "reason", "root_cause": [...]}}, or, for
// some errors, {"error": "<message>"}.
func (c *client) readStatusError(code int, body io.Reader) error {
	e := &statusError{product: c.product, code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Error json.RawMessage `json:"error"`
	}

	if json.Unmarshal(data, &parsed) != nil || len(parsed.Error) == 0 {
		return e
	}

	var text string
	if json.Unmarshal(parsed.Error, &text) == nil {
		e.message = source.CutText(text, maxMessageLen)

		return e
	}

	var detail struct {
		errorCause
		RootCause []errorCause `json:"root_cause"`
	}

	if json.Unmarshal(parsed.Error, &detail) != nil {
		return e
	}

	e.kind = source.CutText(detail.Type, maxNameLen)
	message := detail.Reason

	if len(detail.RootCause) > 0 && detail.RootCause[0] != detail.errorCause {
		message += "; caused by " + detail.RootCause[0].Type + ": " + detail.RootCause[0].Reason
	}

	e.message = source.CutText(message, maxMessageLen)

	return e
}

// header returns the Authorization header of a request.
func (a auth) header(ctx context.Context) (string, error) {
	if a.apiKey != nil {
		key, err := a.read(ctx, *a.apiKey)
		if err != nil {
			return "", fmt.Errorf("read the API key: %w", err)
		}

		return "ApiKey " + key, nil
	}

	password, err := a.read(ctx, a.password)
	if err != nil {
		return "", fmt.Errorf("read the password: %w", err)
	}

	return "Basic " + base64.StdEncoding.EncodeToString([]byte(a.username+":"+password)), nil
}

// read returns the value of a secret without the spaces around it.
func (a auth) read(ctx context.Context, ref registry.SecretRef) (string, error) {
	raw, err := a.secrets.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", ref.Name, ref.Key, err)
	}

	value := string(bytes.TrimSpace(raw))
	if value == "" {
		return "", fmt.Errorf("secret %s/%s: %w", ref.Name, ref.Key, errEmptySecret)
	}

	return value, nil
}

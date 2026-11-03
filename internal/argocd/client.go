package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// DefaultNamespace is the namespace Argo CD runs in, and so the namespace
// of the applications the client reads, unless Options says otherwise.
const DefaultNamespace = "argocd"

// maxResponseSize bounds one response body. A list of applications carries
// the full status of each, so it is the largest answer.
const maxResponseSize = 32 << 20

// maxErrorBodySize bounds the part of an error answer that is read for
// Argo CD's message.
const maxErrorBodySize = 64 << 10

// maxMessageLen bounds the message of a StatusError.
const maxMessageLen = 2 << 10

// apiPath is the root of Argo CD's REST API, under the base URL.
const apiPath = "/api/v1/applications"

var (
	// ErrEmptyToken is returned when the token secret holds nothing.
	ErrEmptyToken = errors.New("the token secret is empty")

	errNoSecrets        = errors.New("no secrets resolver")
	errNoBaseURL        = errors.New("baseURL: required")
	errResponseTooLarge = fmt.Errorf("the response from Argo CD is larger than %d bytes; list fewer projects in applications", maxResponseSize)
	errOtherNamespace   = errors.New("the application is not in the namespace Sidekick reads")
)

// Options are what New needs.
type Options struct {
	// BaseURL is the root of the Argo CD server, with its sub-path when it
	// is served from one. It must be https, or http on a loopback host.
	BaseURL string
	// TokenSecret holds the API token. It is read on every request, so a
	// rotated token needs no restart, and it is never logged.
	TokenSecret registry.SecretRef
	// Namespace is the namespace of the applications to read. Empty means
	// DefaultNamespace.
	Namespace string
	// HTTP is the HTTP client. Nil means httpclient.New().
	HTTP *http.Client
	// Secrets resolves TokenSecret.
	Secrets registry.Secrets
	// UserAgent is sent with every request. Empty means "sidekick/" and the
	// version.
	UserAgent string
}

// Client sends requests to one Argo CD server's REST API with a Bearer
// token. It is safe for concurrent use.
type Client struct {
	http      *http.Client
	baseURL   string
	namespace string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
	// maxResponse is maxResponseSize; tests lower it.
	maxResponse int64
}

// StatusError is an answer of Argo CD that is not a success.
type StatusError struct {
	Code int
	// Message is Argo CD's own error message. It is untrusted and cut.
	Message string
}

// Error returns the status and Argo CD's message.
func (e *StatusError) Error() string {
	text := "Argo CD answered " + strconv.Itoa(e.Code)
	if e.Message != "" {
		text += ": " + e.Message
	}

	return text
}

// GetApplication reads one application, naming its project in the request.
// A missing application, and one in another project, is a *StatusError
// with Code 404. One that the token may not get is a *StatusError with
// Code 403.
func (c *Client) GetApplication(ctx context.Context, ref AppRef) (*Application, error) {
	query := url.Values{"appNamespace": {c.namespace}, "project": {ref.Project}}

	var app Application

	if err := c.get(ctx, apiPath+"/"+url.PathEscape(ref.Name), query, &app); err != nil {
		return nil, fmt.Errorf("get application %s: %w", ref, err)
	}

	if app.Metadata.Namespace != c.namespace {
		return nil, fmt.Errorf("get application %s: %w", ref, errOtherNamespace)
	}

	return &app, nil
}

// ListQuery narrows a list of applications.
type ListQuery struct {
	// Projects are the projects to list. Empty means every project the
	// token may read.
	Projects []string
}

// ListApplications lists the applications of the client's namespace that
// the token may get. Argo CD leaves out the ones it may not, and the client
// leaves out any that are in another namespace.
func (c *Client) ListApplications(ctx context.Context, q ListQuery) ([]Application, error) {
	query := url.Values{"appNamespace": {c.namespace}}
	if len(q.Projects) > 0 {
		query["projects"] = q.Projects
	}

	var list struct {
		Items []Application `json:"items"`
	}

	if err := c.get(ctx, apiPath, query, &list); err != nil {
		return nil, fmt.Errorf("list applications: %w", err)
	}

	apps := make([]Application, 0, len(list.Items))

	for _, app := range list.Items {
		if app.Metadata.Namespace == c.namespace {
			apps = append(apps, app)
		}
	}

	return apps, nil
}

// RevisionMetadata reads the commit message, author, date and tags of a
// revision of an application. historyID is the ID of the history entry
// that holds the revision, so Argo CD reads it from the repository of that
// entry.
func (c *Client) RevisionMetadata(ctx context.Context, ref AppRef, revision string, historyID int64) (*RevisionMetadata, error) {
	query := url.Values{
		"appNamespace": {c.namespace},
		"project":      {ref.Project},
		"versionId":    {strconv.FormatInt(historyID, 10)},
	}

	var meta RevisionMetadata

	path := apiPath + "/" + url.PathEscape(ref.Name) + "/revisions/" + url.PathEscape(revision) + "/metadata"
	if err := c.get(ctx, path, query, &meta); err != nil {
		return nil, fmt.Errorf("get metadata of revision %s of application %s: %w", revision, ref, err)
	}

	return &meta, nil
}

// ApplicationURL returns the link to the application's page in the Argo CD
// UI.
func (c *Client) ApplicationURL(ref AppRef) string {
	return c.baseURL + "/applications/" + url.PathEscape(c.namespace) + "/" + url.PathEscape(ref.Name)
}

// rollbackRequest is the body of a rollback call. Only the fields Sidekick
// sets are here: Prune stays false, so a rollback never deletes anything,
// and no dry run is asked for.
type rollbackRequest struct {
	ID           int64  `json:"id"`
	Project      string `json:"project"`
	AppNamespace string `json:"appNamespace"`
	Prune        bool   `json:"prune"`
}

// Rollback asks Argo CD to put the application back on the history entry
// with the given ID, as the Argo CD rollback call does: it deploys the
// entry's own source and revision, skips hooks, and does not prune. Argo CD
// answers as soon as the operation is requested, with the application, so
// the caller reads the application again to see the operation end. A
// missing history entry, an application with auto-sync on and an operation
// that runs already are 4xx answers, as a *StatusError.
func (c *Client) Rollback(ctx context.Context, ref AppRef, historyID int64) (*Application, error) {
	body := rollbackRequest{ID: historyID, Project: ref.Project, AppNamespace: c.namespace}

	var app Application

	if err := c.post(ctx, apiPath+"/"+url.PathEscape(ref.Name)+"/rollback", body, &app); err != nil {
		return nil, fmt.Errorf("roll back application %s to history entry %d: %w", ref, historyID, err)
	}

	return &app, nil
}

// get sends a GET request for path, under the base URL, with query, and
// decodes the JSON answer into out. An answer that is not a success returns
// a *StatusError.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.send(ctx, http.MethodGet, path, query, nil, out)
}

// post sends body as JSON in a POST request for path, under the base URL,
// and decodes the JSON answer into out. An answer that is not a success
// returns a *StatusError.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	return c.send(ctx, http.MethodPost, path, nil, data, out)
}

// send sends one request with the token and decodes the JSON answer into
// out. body is nil, or the JSON of the request.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	token, err := c.readToken(ctx)
	if err != nil {
		return fmt.Errorf("read the token: %w", err)
	}

	target := c.baseURL + path
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

	answer := httpclient.LimitBody(resp.Body, c.maxResponse, errResponseTooLarge)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return readStatusError(resp.StatusCode, answer)
	}

	if err := json.NewDecoder(answer).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w", err)
	}

	return nil
}

// readToken returns the token from its secret.
func (c *Client) readToken(ctx context.Context) (string, error) {
	raw, err := c.secrets.Resolve(ctx, c.token)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", c.token.Name, c.token.Key, err)
	}

	token := string(bytes.TrimSpace(raw))
	if token == "" {
		return "", ErrEmptyToken
	}

	return token, nil
}

// New builds a client. It makes no network call.
func New(o Options) (*Client, error) {
	if o.Secrets == nil {
		return nil, errNoSecrets
	}

	if o.BaseURL == "" {
		return nil, errNoBaseURL
	}

	if err := CheckBaseURL(o.BaseURL); err != nil {
		return nil, fmt.Errorf("check the base URL: %w", err)
	}

	c := &Client{
		http:      o.HTTP,
		baseURL:   strings.TrimSuffix(o.BaseURL, "/"),
		namespace: o.Namespace,
		userAgent: o.UserAgent,
		token:     o.TokenSecret,
		secrets:   o.Secrets,

		maxResponse: maxResponseSize,
	}

	if c.http == nil {
		c.http = httpclient.New()
	}

	if c.namespace == "" {
		c.namespace = DefaultNamespace
	}

	if c.userAgent == "" {
		c.userAgent = "sidekick/" + buildinfo.Version
	}

	return c, nil
}

// CheckBaseURL checks that raw is an absolute https URL with no query,
// fragment or user info, so the token never crosses the network
// unencrypted. Plain http is allowed only for a loopback host, such as a
// local test server. The error starts with the key "baseURL".
func CheckBaseURL(raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("baseURL: %q must be an absolute https URL, such as https://argocd.example.com", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("baseURL: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}

// readStatusError returns the *StatusError of an answer with code, with
// Argo CD's message when the body holds one.
func readStatusError(code int, body io.Reader) error {
	e := &StatusError{Code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}

	if json.Unmarshal(data, &parsed) == nil {
		msg := parsed.Message
		if msg == "" {
			msg = parsed.Error
		}

		e.Message = source.CutText(msg, maxMessageLen)
	}

	return e
}

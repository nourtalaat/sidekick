package gcprun

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
	"time"

	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/source"
)

const (
	// maxResponseSize bounds one response body.
	maxResponseSize = 16 << 20
	// maxErrorBodySize bounds the part of an error answer that is read for
	// Google Cloud's message.
	maxErrorBodySize = 64 << 10
	// maxMessageLen bounds the message of Google Cloud that is kept.
	maxMessageLen = 512
	// revisionPageSize is how many revisions one list call asks for.
	revisionPageSize = 100
)

// The codes of google.rpc.Status that an operation error can carry and that
// the action reads.
const (
	codeInvalidArgument    = 3
	codeNotFound           = 5
	codePermissionDenied   = 7
	codeFailedPrecondition = 9
	codeAborted            = 10
)

// Allocation types of a traffic entry, as the API writes them.
const (
	allocationLatest   = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
	allocationRevision = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
)

// The condition of a revision that says it is ready.
const (
	conditionReady     = "Ready"
	conditionSucceeded = "CONDITION_SUCCEEDED"
)

// errResponseTooLarge is returned when a response body is larger than
// maxResponseSize.
var errResponseTooLarge = fmt.Errorf("the response from Google Cloud is larger than %d bytes", maxResponseSize)

// serviceRef names one Cloud Run service.
type serviceRef struct {
	project  string
	location string
	name     string
}

// path returns the resource name of the service.
func (s serviceRef) path() string {
	return "projects/" + s.project + "/locations/" + s.location + "/services/" + s.name
}

// target is the Snapshot.Target of a rollback of the service.
func (s serviceRef) target() string {
	return "gcp:cloudrun/" + s.project + "/" + s.location + "/" + s.name
}

// trafficTarget is one entry of the traffic list of a service.
type trafficTarget struct {
	Type     string `json:"type,omitempty"`
	Revision string `json:"revision,omitempty"`
	Percent  int32  `json:"percent"`
	Tag      string `json:"tag,omitempty"`
}

// service is a Cloud Run service as the action reads it. raw is the whole
// answer, so an update can send the service back as it was read.
type service struct {
	Name                string          `json:"name"`
	Etag                string          `json:"etag"`
	LastModifier        string          `json:"lastModifier"`
	LatestReadyRevision string          `json:"latestReadyRevision"`
	Reconciling         bool            `json:"reconciling"`
	Traffic             []trafficTarget `json:"traffic"`

	raw map[string]json.RawMessage
}

// revision is a Cloud Run revision as the action reads it.
type revision struct {
	Name       string `json:"name"`
	CreateTime string `json:"createTime"`
	Creator    string `json:"creator"`
	Containers []struct {
		Image string `json:"image"`
	} `json:"containers"`
	Conditions []struct {
		Type  string `json:"type"`
		State string `json:"state"`
	} `json:"conditions"`
}

// revisionPage is one page of the list of revisions.
type revisionPage struct {
	Revisions     []revision `json:"revisions"`
	NextPageToken string     `json:"nextPageToken"`
}

// operation is a long-running operation.
type operation struct {
	Name  string `json:"name"`
	Done  bool   `json:"done"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// client sends requests to the Cloud Run Admin API with the action's tokens.
type client struct {
	http      *http.Client
	tokens    oauth2.TokenSource
	userAgent string
	baseURL   string
}

// statusError is an answer of Google Cloud that is not a success.
type statusError struct {
	code int
	// status is the gRPC status name Google Cloud sends, such as
	// PERMISSION_DENIED. It may be empty.
	status string
	// message is Google Cloud's own error message, untrusted, cut.
	message string
}

// Error implements error.
func (e *statusError) Error() string {
	text := fmt.Sprintf("Google Cloud answered %d", e.code)
	if e.status != "" {
		text += " " + e.status
	}

	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// service reads the service.
func (c *client) service(ctx context.Context, ref serviceRef) (service, error) {
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/v2/"+ref.path(), nil, nil, &raw); err != nil {
		return service{}, fmt.Errorf("read service %s: %w", ref.path(), err)
	}

	var svc service
	if err := json.Unmarshal(raw, &svc); err != nil {
		return service{}, fmt.Errorf("decode service %s: %w", ref.path(), err)
	}

	if err := json.Unmarshal(raw, &svc.raw); err != nil {
		return service{}, fmt.Errorf("decode service %s: %w", ref.path(), err)
	}

	return svc, nil
}

// revisions reads the revisions of the service, newest first as Google
// Cloud sends them, up to pages pages. It reports whether there are more.
func (c *client) revisions(ctx context.Context, ref serviceRef, pages int) ([]revision, bool, error) {
	var all []revision

	token := ""

	for range pages {
		query := url.Values{"pageSize": {strconv.Itoa(revisionPageSize)}}
		if token != "" {
			query.Set("pageToken", token)
		}

		var page revisionPage
		if err := c.do(ctx, http.MethodGet, "/v2/"+ref.path()+"/revisions", query, nil, &page); err != nil {
			return nil, false, fmt.Errorf("list the revisions of %s: %w", ref.path(), err)
		}

		all = append(all, page.Revisions...)

		if page.NextPageToken == "" {
			return all, false, nil
		}

		token = page.NextPageToken
	}

	return all, true, nil
}

// setTraffic updates only the traffic of the service. The body is the service
// as it was read, with its traffic replaced, and the update mask limits the
// change to the traffic.
func (c *client) setTraffic(ctx context.Context, ref serviceRef, svc service, traffic []trafficTarget) (operation, error) {
	encoded, err := json.Marshal(traffic)
	if err != nil {
		return operation{}, fmt.Errorf("encode the traffic: %w", err)
	}

	body := make(map[string]json.RawMessage, len(svc.raw)+1)
	for k, v := range svc.raw {
		body[k] = v
	}

	body["traffic"] = encoded

	data, err := json.Marshal(body)
	if err != nil {
		return operation{}, fmt.Errorf("encode the service: %w", err)
	}

	var op operation
	if err := c.do(ctx, http.MethodPatch, "/v2/"+ref.path(), url.Values{"updateMask": {"traffic"}}, data, &op); err != nil {
		return operation{}, fmt.Errorf("update the traffic of %s: %w", ref.path(), err)
	}

	return op, nil
}

// operation reads a long-running operation by its name.
func (c *client) operation(ctx context.Context, name string) (operation, error) {
	var op operation
	if err := c.do(ctx, http.MethodGet, "/v2/"+name, nil, nil, &op); err != nil {
		return operation{}, fmt.Errorf("read operation %s: %w", name, err)
	}

	return op, nil
}

// do sends one request with a token and decodes the JSON answer into out. An
// answer that is not a success returns a *statusError.
func (c *client) do(ctx context.Context, method, apiPath string, query url.Values, body []byte, out any) error {
	token, err := c.tokens.Token()
	if err != nil {
		return fmt.Errorf("get an access token: %w", err)
	}

	target := c.baseURL + apiPath
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
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("User-Agent", c.userAgent)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return readStatusError(resp.StatusCode, resp.Body)
	}

	limited := httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return fmt.Errorf("decode the answer: %w", err)
	}

	return nil
}

// readStatusError returns the *statusError of an answer with code. Google
// Cloud sends {"error": {"code", "message", "status"}}.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return e
	}

	e.message = source.CutText(parsed.Error.Message, maxMessageLen)
	e.status = source.CutText(parsed.Error.Status, maxMessageLen)

	return e
}

// classify wraps an error of a call that Execute or Undo makes, so Sidekick
// knows whether to try again: a stale etag is a conflict, a request that
// Google Cloud refuses for good is permanent, and anything else, such as a
// network error or a 5xx answer, is left for Sidekick to try again.
func classify(err error) error {
	se, ok := errors.AsType[*statusError](err)
	if !ok {
		return fmt.Errorf("the call did not finish: %w", err)
	}

	switch se.code {
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", action.ErrConflict, err)
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("the call failed, and may work later: %w", err)
	}
}

// operationError returns the error of a finished operation, classified the
// same way as classify.
func operationError(code int, message string) error {
	err := fmt.Errorf("the operation failed with code %d: %s", code, source.CutText(message, maxMessageLen))

	switch code {
	case codeAborted:
		return fmt.Errorf("%w: %w", action.ErrConflict, err)
	case codeInvalidArgument, codeNotFound, codePermissionDenied, codeFailedPrecondition:
		return fmt.Errorf("%w: %w", action.ErrPermanent, err)
	default:
		return fmt.Errorf("the operation failed, and the update may work later: %w", err)
	}
}

// wait sleeps for d, and returns early with the error of ctx when it ends.
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for the operation: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// shortName returns the last part of a revision name, which Google Cloud
// writes as a full path in some fields and as a bare name in others.
func shortName(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}

	return name
}

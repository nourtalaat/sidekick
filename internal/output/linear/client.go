package linear

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
)

// linearHost is the host of Linear's web app, where issue links point.
const linearHost = "linear.app"

// The operations of the client, as sidekick_output_api_errors_total names
// them.
const (
	opIssue         = "issue"
	opCommentCreate = "comment_create"
	opCommentUpdate = "comment_update"
)

// The GraphQL documents the client sends.
const (
	queryIssue = `query SidekickIssue($id: String!) {
  issue(id: $id) { id identifier team { key } }
}`
	mutationCommentCreate = `mutation SidekickCommentCreate($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { id } }
}`
	mutationCommentUpdate = `mutation SidekickCommentUpdate($id: String!, $input: CommentUpdateInput!) {
  commentUpdate(id: $id, input: $input) { success comment { id } }
}`
)

const (
	// codeRateLimited is the extensions.code of a request over Linear's
	// rate limit.
	codeRateLimited = "RATELIMITED"
	// notFoundPrefix starts the message of an error about an ID that
	// matches nothing, such as "Entity not found: Issue".
	notFoundPrefix = "Entity not found"
)

// codePattern is what an error code looks like. Other codes are reported
// as codeOther, so a server cannot put text of its own into an error.
var codePattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)

// The bits of a version 4 UUID that commentID sets (RFC 9562).
const (
	uuidVersionByte = 6
	uuidVersionMask = 0xf0
	uuidVersion4    = 0x40
	uuidVariantByte = 8
	uuidVariantMask = 0xc0
	uuidVariantRFC  = 0x80
)

// codeOther stands for an error code that does not match codePattern.
const codeOther = "OTHER"

var (
	errUnsuccessful = errors.New("the answer does not report success")
	errNoComment    = errors.New("the answer has no comment")
)

// apiError is a GraphQL response with errors. It holds only the first
// error's code, never its message, which may repeat the comment text.
type apiError struct {
	Code string
}

// Error gives Linear's error code; the caller names the operation.
func (e *apiError) Error() string {
	return "Linear answered " + e.Code
}

// client calls Linear's GraphQL API and implements ticket.Tracker.
type client struct {
	apiURL    string
	workspace string
	http      *sinkhttp.Client
	apiKey    func(ctx context.Context) (string, error)
}

var _ ticket.Tracker = (*client)(nil)

// request is a GraphQL request.
type request struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// gqlError is one entry of a GraphQL response's errors.
type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

// commentResult is the payload of commentCreate and commentUpdate.
type commentResult struct {
	Success bool `json:"success"`
	Comment *struct {
		ID string `json:"id"`
	} `json:"comment"`
}

// KeyFromURL reads an issue link of the workspace, such as
// https://linear.app/acme/issue/OPS-123/checkout-is-slow.
func (c *client) KeyFromURL(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), linearHost) {
		return "", false
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], c.workspace) || parts[1] != "issue" {
		return "", false
	}

	return ticket.Key(parts[2])
}

// Issue looks up the issue with the identifier key.
func (c *client) Issue(ctx context.Context, key string) (ticket.Issue, error) {
	var out struct {
		Issue *struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			Team       struct {
				Key string `json:"key"`
			} `json:"team"`
		} `json:"issue"`
	}

	err := c.call(ctx, opIssue, request{Query: queryIssue, Variables: map[string]any{"id": key}}, &out)
	if err != nil {
		return ticket.Issue{}, fmt.Errorf("look up issue %s: %w", key, err)
	}

	if out.Issue == nil {
		return ticket.Issue{}, fmt.Errorf("look up issue %s: %w", key, ticket.ErrNotFound)
	}

	return ticket.Issue{ID: out.Issue.ID, Key: out.Issue.Identifier, Scope: out.Issue.Team.Key}, nil
}

// CreateComment adds the comment with an ID derived from its idempotency
// key. When Linear refuses, it tries to edit a comment with that ID, which
// is there when an earlier attempt went through without its answer
// arriving.
func (c *client) CreateComment(ctx context.Context, issue ticket.Issue, comment ticket.Comment) (string, error) {
	id := commentID(comment.IdempotencyKey)
	body := render(comment.Doc)

	var out struct {
		CommentCreate commentResult `json:"commentCreate"`
	}

	err := c.call(ctx, opCommentCreate, request{
		Query:     mutationCommentCreate,
		Variables: map[string]any{"input": map[string]string{"id": id, "issueId": issue.ID, "body": body}},
	}, &out)
	if err == nil {
		return checkComment(out.CommentCreate)
	}

	if !mayExist(err) {
		return "", fmt.Errorf("create the comment: %w", err)
	}

	if updateErr := c.update(ctx, id, body); updateErr != nil {
		return "", fmt.Errorf("create the comment: %w", errors.Join(err, fmt.Errorf("edit an earlier attempt: %w", updateErr)))
	}

	return id, nil
}

// UpdateComment replaces the body of the comment.
func (c *client) UpdateComment(ctx context.Context, _ ticket.Issue, commentID string, comment ticket.Comment) error {
	if err := c.update(ctx, commentID, render(comment.Doc)); err != nil {
		return fmt.Errorf("update the comment: %w", err)
	}

	return nil
}

// update sets the body of the comment with id.
func (c *client) update(ctx context.Context, id, body string) error {
	var out struct {
		CommentUpdate commentResult `json:"commentUpdate"`
	}

	err := c.call(ctx, opCommentUpdate, request{
		Query:     mutationCommentUpdate,
		Variables: map[string]any{"id": id, "input": map[string]string{"body": body}},
	}, &out)
	if err != nil {
		return fmt.Errorf("update comment %s: %w", id, err)
	}

	if _, err := checkComment(out.CommentUpdate); err != nil {
		return fmt.Errorf("check the answer: %w", err)
	}

	return nil
}

// call sends one GraphQL request and decodes its data into out. A response
// with errors returns an *apiError, wrapping ticket.ErrNotFound for an ID
// that matches nothing and sinkhttp.ErrRateLimited for a rate limit.
func (c *client) call(ctx context.Context, operation string, req request, out any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	key, err := c.apiKey(ctx)
	if err != nil {
		return fmt.Errorf("read the API key: %w", err)
	}

	resp, err := c.http.Do(ctx, operation, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("make the request: %w", err)
		}

		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", key)

		return r, nil
	})
	if err != nil {
		return fmt.Errorf("call Linear: %w", err)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}

	if jsonErr := json.Unmarshal(resp.Body, &envelope); jsonErr != nil {
		if statusErr := resp.Err(); statusErr != nil {
			return fmt.Errorf("call Linear: %w", statusErr)
		}

		c.http.Failed(operation, sinkhttp.ReasonBadResponse)

		return fmt.Errorf("decode the response: %w", jsonErr)
	}

	if len(envelope.Errors) > 0 {
		c.http.Failed(operation, sinkhttp.ReasonAPIError)

		return errorOf(envelope.Errors[0])
	}

	if statusErr := resp.Err(); statusErr != nil {
		return fmt.Errorf("call Linear: %w", statusErr)
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		c.http.Failed(operation, sinkhttp.ReasonBadResponse)

		return fmt.Errorf("decode the response data: %w", err)
	}

	return nil
}

// newClient returns a client for the workspace that sends requests to
// apiURL.
func newClient(apiURL, workspace string, hc *sinkhttp.Client, apiKey func(ctx context.Context) (string, error)) *client {
	return &client{apiURL: apiURL, workspace: workspace, http: hc, apiKey: apiKey}
}

// errorOf turns a GraphQL error into an *apiError with only its code,
// wrapped with the sentinel it stands for.
func errorOf(e gqlError) error {
	code := e.Extensions.Code
	if !codePattern.MatchString(code) {
		code = codeOther
	}

	apiErr := &apiError{Code: code}

	switch {
	case code == codeRateLimited:
		return fmt.Errorf("%w: %w", apiErr, sinkhttp.ErrRateLimited)
	case strings.HasPrefix(e.Message, notFoundPrefix):
		return fmt.Errorf("%w: %w", apiErr, ticket.ErrNotFound)
	default:
		return apiErr
	}
}

// mayExist reports whether a failed commentCreate may have failed because
// the comment exists already: Linear refused it, and not for a rate limit
// or an issue that is gone.
func mayExist(err error) bool {
	_, refused := errors.AsType[*apiError](err)

	return refused && !errors.Is(err, ticket.ErrNotFound) && !errors.Is(err, sinkhttp.ErrRateLimited)
}

// checkComment returns the ID of the comment a mutation returned.
func checkComment(r commentResult) (string, error) {
	if !r.Success {
		return "", errUnsuccessful
	}

	if r.Comment == nil || r.Comment.ID == "" {
		return "", errNoComment
	}

	return r.Comment.ID, nil
}

// commentID derives the ID of a comment from its idempotency key: a
// UUID in version 4 form, as Linear expects for an ID chosen by the
// client, made from the key's SHA-256 sum so every attempt uses the same.
func commentID(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(idempotencyKey))

	var id uuid.UUID

	copy(id[:], sum[:len(id)])
	id[uuidVersionByte] = id[uuidVersionByte]&^uuidVersionMask | uuidVersion4
	id[uuidVariantByte] = id[uuidVariantByte]&^uuidVariantMask | uuidVariantRFC

	return id.String()
}

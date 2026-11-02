package linear

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// maxAnswerBytes is the most of an answer the client reads.
const maxAnswerBytes = 1 << 20

// apiKeyPrefix starts a personal API key, which Linear wants in the
// Authorization header as it is. Any other token is an OAuth token, sent as
// a bearer token.
const apiKeyPrefix = "lin_api_"

// Codes in the extensions of a GraphQL error that a repeat may fix.
const (
	codeRateLimited = "RATELIMITED"
	codeInternal    = "INTERNAL_SERVER_ERROR"
	codeUnavailable = "SERVICE_UNAVAILABLE"
	// codeOther stands for a code that does not match codePattern, so a
	// server cannot put text of its own into an error.
	codeOther = "OTHER"
	// notFoundPrefix starts the message of an error about an ID that
	// matches nothing, such as "Entity not found: Issue".
	notFoundPrefix = "Entity not found"
)

// codePattern is what an error code looks like.
var codePattern = regexp.MustCompile(`^[A-Z][A-Z_]{0,39}$`)

// The GraphQL documents the client sends.
const (
	queryIssue = `query SidekickIssue($id: String!) {
  issue(id: $id) { id identifier url team { id key } }
}`
	queryComment = `query SidekickComment($id: String!) {
  comment(id: $id) { id url }
}`
	mutationCommentCreate = `mutation SidekickCommentCreate($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { id url } }
}`
)

var (
	errNotFound     = errors.New("no such item in Linear")
	errUnsuccessful = errors.New("the answer does not report success")
	errNoID         = errors.New("the answer has no ID")
	errBadAnswer    = errors.New("the answer cannot be read")
)

// errMissing is what the lookups return for an item that does not exist:
// it wraps errNotFound, and action.ErrPermanent, since asking again does
// not make the item appear.
var errMissing = fmt.Errorf("%w: %w", action.ErrPermanent, errNotFound)

// apiError is a GraphQL response with errors. It holds only the first
// error's code, never its message, which may repeat the text of the request.
type apiError struct {
	Code string
}

// Error gives Linear's error code.
func (e *apiError) Error() string {
	return "Linear answered " + e.Code
}

// client calls Linear's GraphQL API.
type client struct {
	apiURL  string
	http    *http.Client
	secrets registry.Secrets
	key     registry.SecretRef
}

type teamRef struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type issueRef struct {
	ID         string  `json:"id"`
	Identifier string  `json:"identifier"`
	URL        string  `json:"url"`
	Team       teamRef `json:"team"`
}

type commentRef struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// issue looks up an issue by its identifier, such as OPS-123, or its ID. It
// returns an error wrapping errNotFound when there is none.
func (c *client) issue(ctx context.Context, id string) (issueRef, error) {
	var out struct {
		Issue *issueRef `json:"issue"`
	}

	if err := c.call(ctx, queryIssue, map[string]any{"id": id}, &out); err != nil {
		return issueRef{}, fmt.Errorf("look up issue: %w", err)
	}

	if out.Issue == nil {
		return issueRef{}, fmt.Errorf("look up issue: %w", errMissing)
	}

	return *out.Issue, nil
}

// comment looks up a comment by its ID. It returns an error wrapping
// errNotFound when there is none.
func (c *client) comment(ctx context.Context, id string) (commentRef, error) {
	var out struct {
		Comment *commentRef `json:"comment"`
	}

	if err := c.call(ctx, queryComment, map[string]any{"id": id}, &out); err != nil {
		return commentRef{}, fmt.Errorf("look up comment: %w", err)
	}

	if out.Comment == nil {
		return commentRef{}, fmt.Errorf("look up comment: %w", errMissing)
	}

	return *out.Comment, nil
}

// createComment adds a comment with the given ID to the issue.
func (c *client) createComment(ctx context.Context, id, issueID, body string) (commentRef, error) {
	var out struct {
		CommentCreate struct {
			Success bool        `json:"success"`
			Comment *commentRef `json:"comment"`
		} `json:"commentCreate"`
	}

	input := map[string]string{"id": id, "issueId": issueID, "body": body}
	if err := c.call(ctx, mutationCommentCreate, map[string]any{"input": input}, &out); err != nil {
		return commentRef{}, fmt.Errorf("create the comment: %w", err)
	}

	if !out.CommentCreate.Success {
		return commentRef{}, fmt.Errorf("create the comment: %w", errUnsuccessful)
	}

	if out.CommentCreate.Comment == nil || out.CommentCreate.Comment.ID == "" {
		return commentRef{}, fmt.Errorf("create the comment: %w", errNoID)
	}

	return *out.CommentCreate.Comment, nil
}

// call sends one GraphQL request and decodes its data into out. A rate
// limit, a server error and a transport failure are errors Sidekick tries
// again; every other failure wraps action.ErrPermanent.
func (c *client) call(ctx context.Context, query string, variables map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fmt.Errorf("encode the request: %w", err)
	}

	token, err := secrets.ResolveText(ctx, c.secrets, c.key)
	if err != nil {
		return fmt.Errorf("read apiKeySecret: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorization(token))

	resp, err := actionhttp.Do(c.http, req, maxAnswerBytes)
	if err != nil {
		return fmt.Errorf("call Linear: %w", err)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(resp.Body, &envelope); err != nil {
		if statusErr := resp.Err(); statusErr != nil {
			return fmt.Errorf("call Linear: %w", statusErr)
		}

		return fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadAnswer, err)
	}

	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]

		return errorOf(first.Extensions.Code, first.Message)
	}

	if statusErr := resp.Err(); statusErr != nil {
		return fmt.Errorf("call Linear: %w", statusErr)
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadAnswer, err)
	}

	return nil
}

// authorization returns the Authorization header for a token.
func authorization(token string) string {
	if strings.HasPrefix(token, apiKeyPrefix) {
		return token
	}

	return "Bearer " + token
}

// errorOf turns a GraphQL error into an *apiError with only its code. A
// rate limit and a server error can pass when repeated; any other error
// wraps action.ErrPermanent, and an ID that matches nothing wraps
// errNotFound too.
func errorOf(code, message string) error {
	if !codePattern.MatchString(code) {
		code = codeOther
	}

	apiErr := &apiError{Code: code}

	switch {
	case code == codeRateLimited || code == codeInternal || code == codeUnavailable:
		return apiErr
	case strings.HasPrefix(message, notFoundPrefix):
		return fmt.Errorf("%w: %w: %w", action.ErrPermanent, errNotFound, apiErr)
	default:
		return fmt.Errorf("%w: %w", action.ErrPermanent, apiErr)
	}
}

// deriveUUID makes a version 4 UUID from the SHA-256 sum of key, the form
// Linear expects for an ID chosen by the client. The same key always gives
// the same ID.
func deriveUUID(key string) string {
	const (
		versionByte = 6
		versionMask = 0xf0
		version4    = 0x40
		variantByte = 8
		variantMask = 0xc0
		variantRFC  = 0x80
	)

	sum := sha256.Sum256([]byte(key))

	var id uuid.UUID

	copy(id[:], sum[:len(id)])
	id[versionByte] = id[versionByte]&^versionMask | version4
	id[variantByte] = id[variantByte]&^variantMask | variantRFC

	return id.String()
}

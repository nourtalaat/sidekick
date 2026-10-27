package jira

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

	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
)

// The operations of the client, as sidekick_output_api_errors_total names
// them.
const (
	opIssue         = "issue"
	opListComments  = "comment_list"
	opCommentCreate = "comment_create"
	opCommentUpdate = "comment_update"
)

const (
	// apiPath is where the REST API version 3 starts, below the site URL.
	apiPath = "/rest/api/3"
	// browsePath starts the path of an issue link, below the site URL.
	browsePath = "/browse/"
	// propertyKey is the key of the comment property that holds the
	// idempotency key of a comment Sidekick posted.
	propertyKey = "sidekick"
	// recentComments is how many of an issue's newest comments CreateComment
	// looks through for one an earlier attempt posted.
	recentComments = 50
)

var (
	errBadSiteURL = errors.New("url: expected an absolute URL")
	errNoID       = errors.New("the answer has no ID")
)

// client calls the REST API of one Jira Cloud site and implements
// ticket.Tracker.
type client struct {
	site  *url.URL
	email string
	http  *sinkhttp.Client
	token func(ctx context.Context) (string, error)
}

var _ ticket.Tracker = (*client)(nil)

// commentBody is the body of a new or edited comment.
type commentBody struct {
	Body       node       `json:"body"`
	Properties []property `json:"properties,omitempty"`
}

// property is an entity property of a comment.
type property struct {
	Key   string        `json:"key"`
	Value propertyValue `json:"value"`
}

// propertyValue is the value of the sidekick property.
type propertyValue struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

// KeyFromURL reads an issue link of the site, such as
// https://acme.atlassian.net/browse/OPS-123.
func (c *client) KeyFromURL(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme != c.site.Scheme || !strings.EqualFold(u.Host, c.site.Host) {
		return "", false
	}

	rest, ok := strings.CutPrefix(u.Path, strings.TrimSuffix(c.site.Path, "/")+browsePath)
	if !ok {
		return "", false
	}

	return ticket.Key(strings.TrimSuffix(rest, "/"))
}

// Issue looks up the issue with key.
func (c *client) Issue(ctx context.Context, key string) (ticket.Issue, error) {
	var out struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	}

	query := url.Values{"fields": {"project"}}
	if err := c.call(ctx, opIssue, http.MethodGet, "/issue/"+url.PathEscape(key), query, nil, &out); err != nil {
		return ticket.Issue{}, fmt.Errorf("look up issue %s: %w", key, err)
	}

	if out.ID == "" {
		return ticket.Issue{}, fmt.Errorf("look up issue %s: %w", key, errNoID)
	}

	return ticket.Issue{ID: out.ID, Key: out.Key, Scope: out.Fields.Project.Key}, nil
}

// CreateComment adds the comment with its idempotency key as a comment
// property. First it looks through the issue's newest comments for one
// with that key, which an earlier attempt posted when its answer did not
// arrive, and edits that one instead.
func (c *client) CreateComment(ctx context.Context, issue ticket.Issue, comment ticket.Comment) (string, error) {
	existing, err := c.findComment(ctx, issue, comment.IdempotencyKey)
	if err != nil {
		return "", fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if existing != "" {
		if err := c.UpdateComment(ctx, issue, existing, comment); err != nil {
			return "", fmt.Errorf("edit the comment of an earlier attempt: %w", err)
		}

		return existing, nil
	}

	body := commentBody{
		Body:       render(comment.Doc),
		Properties: []property{{Key: propertyKey, Value: propertyValue{IdempotencyKey: comment.IdempotencyKey}}},
	}

	var out struct {
		ID string `json:"id"`
	}

	if err := c.call(ctx, opCommentCreate, http.MethodPost, commentsPath(issue), nil, body, &out); err != nil {
		return "", fmt.Errorf("create the comment: %w", err)
	}

	if out.ID == "" {
		return "", fmt.Errorf("create the comment: %w", errNoID)
	}

	return out.ID, nil
}

// UpdateComment replaces the body of the comment.
func (c *client) UpdateComment(ctx context.Context, issue ticket.Issue, commentID string, comment ticket.Comment) error {
	path := commentsPath(issue) + "/" + url.PathEscape(commentID)

	if err := c.call(ctx, opCommentUpdate, http.MethodPut, path, nil, commentBody{Body: render(comment.Doc)}, nil); err != nil {
		return fmt.Errorf("update comment %s: %w", commentID, err)
	}

	return nil
}

// findComment returns the ID of the newest comment of issue whose
// property holds idempotencyKey, or "" when none of the recentComments
// newest has it.
func (c *client) findComment(ctx context.Context, issue ticket.Issue, idempotencyKey string) (string, error) {
	var out struct {
		Comments []struct {
			ID         string     `json:"id"`
			Properties []property `json:"properties"`
		} `json:"comments"`
	}

	query := url.Values{"orderBy": {"-created"}, "maxResults": {strconv.Itoa(recentComments)}, "expand": {"properties"}}
	if err := c.call(ctx, opListComments, http.MethodGet, commentsPath(issue), query, nil, &out); err != nil {
		return "", fmt.Errorf("list the comments: %w", err)
	}

	for _, comment := range out.Comments {
		for _, p := range comment.Properties {
			if p.Key == propertyKey && p.Value.IdempotencyKey == idempotencyKey {
				return comment.ID, nil
			}
		}
	}

	return "", nil
}

// call sends one request to path below the API and decodes a 2xx
// response into out, when out is set. A 404 returns ticket.ErrNotFound,
// and any other status outside 2xx a *sinkhttp.StatusError.
func (c *client) call(ctx context.Context, operation, method, path string, query url.Values, body, out any) error {
	var payload []byte

	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}
	}

	token, err := c.token(ctx)
	if err != nil {
		return fmt.Errorf("read the API token: %w", err)
	}

	target := c.site.JoinPath(apiPath + path)
	target.RawQuery = query.Encode()

	resp, err := c.http.Do(ctx, operation, func(ctx context.Context) (*http.Request, error) {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}

		r, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
		if err != nil {
			return nil, fmt.Errorf("make the request: %w", err)
		}

		r.SetBasicAuth(c.email, token)
		r.Header.Set("Accept", "application/json")

		if payload != nil {
			r.Header.Set("Content-Type", "application/json")
		}

		return r, nil
	})
	if err != nil {
		return fmt.Errorf("call Jira: %w", err)
	}

	if resp.Status == http.StatusNotFound {
		return fmt.Errorf("call Jira: %w", ticket.ErrNotFound)
	}

	if err := resp.Err(); err != nil {
		return fmt.Errorf("call Jira: %w", err)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(resp.Body, out); err != nil {
		c.http.Failed(operation, sinkhttp.ReasonBadResponse)

		return fmt.Errorf("decode the response: %w", err)
	}

	return nil
}

// newClient returns a client for the site at siteURL that signs in as
// email with the token.
func newClient(siteURL, email string, hc *sinkhttp.Client, token func(ctx context.Context) (string, error)) (*client, error) {
	site, err := url.Parse(siteURL)
	if err != nil || site.Host == "" {
		return nil, errBadSiteURL
	}

	return &client{site: site, email: email, http: hc, token: token}, nil
}

// commentsPath is the path of the comments of issue, by its ID, which
// stays the same when the issue moves to another project.
func commentsPath(issue ticket.Issue) string {
	return "/issue/" + url.PathEscape(issue.ID) + "/comment"
}

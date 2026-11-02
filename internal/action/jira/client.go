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
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// maxAnswerBytes is the most of an answer the client reads.
const maxAnswerBytes = 4 << 20

// apiPath is where the REST API version 3 starts, below the site URL.
const apiPath = "rest/api/3"

// recentComments is how many of an issue's newest comments the search for
// an earlier attempt reads.
const recentComments = 50

var errBadAnswer = errors.New("the answer cannot be read")

// issueRef is an issue as the actions need it.
type issueRef struct {
	ID      string
	Key     string
	Project string
}

// client calls the REST API of one Jira Cloud site.
type client struct {
	site    *url.URL
	email   string
	http    *http.Client
	secrets registry.Secrets
	token   registry.SecretRef
}

// issue reads an issue by key or ID, with its project.
func (c *client) issue(ctx context.Context, ref string) (issueRef, error) {
	var out struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
		} `json:"fields"`
	}

	if err := c.call(ctx, http.MethodGet, "issue/"+ref, url.Values{"fields": {"project"}}, nil, &out); err != nil {
		return issueRef{}, fmt.Errorf("read the issue: %w", err)
	}

	if out.ID == "" || out.Key == "" {
		return issueRef{}, fmt.Errorf("read the issue: %w", errBadAnswer)
	}

	return issueRef{ID: out.ID, Key: out.Key, Project: out.Fields.Project.Key}, nil
}

// findComment returns the ID of the newest comment of the issue, among the
// newest recentComments, whose body holds marker, or "" for none.
func (c *client) findComment(ctx context.Context, issueID, marker string) (string, error) {
	var out struct {
		Comments []struct {
			ID   string          `json:"id"`
			Body json.RawMessage `json:"body"`
		} `json:"comments"`
	}

	query := url.Values{"orderBy": {"-created"}, "maxResults": {fmt.Sprint(recentComments)}}
	if err := c.call(ctx, http.MethodGet, "issue/"+issueID+"/comment", query, nil, &out); err != nil {
		return "", fmt.Errorf("list the comments: %w", err)
	}

	quoted, err := json.Marshal(marker)
	if err != nil {
		return "", fmt.Errorf("encode the marker: %w", err)
	}

	for _, comment := range out.Comments {
		if bytes.Contains(comment.Body, quoted) {
			return comment.ID, nil
		}
	}

	return "", nil
}

// createComment adds a comment and returns its ID.
func (c *client) createComment(ctx context.Context, issueID string, body node) (string, error) {
	var out struct {
		ID string `json:"id"`
	}

	if err := c.call(ctx, http.MethodPost, "issue/"+issueID+"/comment", nil, map[string]any{"body": body}, &out); err != nil {
		return "", fmt.Errorf("create the comment: %w", err)
	}

	if out.ID == "" {
		return "", fmt.Errorf("create the comment: %w", errBadAnswer)
	}

	return out.ID, nil
}

// findIssue returns the issue of the project that has the label, or an
// empty issueRef when there is none. Jira's search can lag by a few seconds
// behind a new issue.
func (c *client) findIssue(ctx context.Context, project, label string) (issueRef, error) {
	var out struct {
		Issues []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"issues"`
	}

	jql := fmt.Sprintf(`project = "%s" AND labels = "%s"`, project, label)
	query := url.Values{"jql": {jql}, "fields": {"summary"}, "maxResults": {"1"}}

	if err := c.call(ctx, http.MethodGet, "search/jql", query, nil, &out); err != nil {
		return issueRef{}, fmt.Errorf("search for the label: %w", err)
	}

	if len(out.Issues) == 0 {
		return issueRef{}, nil
	}

	return issueRef{ID: out.Issues[0].ID, Key: out.Issues[0].Key, Project: project}, nil
}

// issueFields are the fields of a new issue.
type issueFields struct {
	Project     keyed    `json:"project"`
	IssueType   named    `json:"issuetype"`
	Summary     string   `json:"summary"`
	Description node     `json:"description"`
	Labels      []string `json:"labels"`
}

type keyed struct {
	Key string `json:"key"`
}

type named struct {
	Name string `json:"name"`
}

// createIssue creates an issue.
func (c *client) createIssue(ctx context.Context, fields issueFields) (issueRef, error) {
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}

	if err := c.call(ctx, http.MethodPost, "issue", nil, map[string]any{"fields": fields}, &out); err != nil {
		return issueRef{}, fmt.Errorf("create the issue: %w", err)
	}

	if out.ID == "" || out.Key == "" {
		return issueRef{}, fmt.Errorf("create the issue: %w", errBadAnswer)
	}

	return issueRef{ID: out.ID, Key: out.Key, Project: fields.Project.Key}, nil
}

// browseURL returns the address of an issue in the site's web UI.
func (c *client) browseURL(key string) string {
	return c.site.JoinPath("browse", key).String()
}

// call sends one request to path below the API and decodes a 2xx answer into
// out. A timeout, a rate limit and a server error can pass when repeated;
// any other status wraps action.ErrPermanent. The error holds the status
// only, never the body, which may repeat the text of the request.
func (c *client) call(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader = http.NoBody

	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}

		reader = bytes.NewReader(payload)
	}

	token, err := secrets.ResolveText(ctx, c.secrets, c.token)
	if err != nil {
		return fmt.Errorf("read apiTokenSecret: %w", err)
	}

	target := c.site.JoinPath(strings.Split(apiPath+"/"+path, "/")...)
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.SetBasicAuth(c.email, token)
	req.Header.Set("Accept", "application/json")

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := actionhttp.Do(c.http, req, maxAnswerBytes)
	if err != nil {
		return fmt.Errorf("call Jira: %w", err)
	}

	if err := resp.Err(); err != nil {
		return fmt.Errorf("call Jira: %w", err)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadAnswer, err)
	}

	return nil
}

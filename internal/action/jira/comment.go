package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// markerPrefix starts the last line of a comment Sidekick posted. The line
// holds a short hash of the idempotency key, so a repeat can find the comment.
const markerPrefix = "sidekick:"

// issueRefSchema is a regular expression for the issue of a comment: its
// key, such as OPS-123, or its numeric ID.
const issueRefSchema = `^([A-Za-z][A-Za-z0-9_]{0,19}-[0-9]{1,9}|[0-9]{1,18})$`

var (
	errBadIssue  = errors.New("issue: expected a key such as OPS-123, or an issue ID")
	errEmptyBody = errors.New("body: required")
	errLongBody  = errors.New("body: too long")
	errProject   = errors.New("the issue is not in one of limits.projects")
)

// commentSchema is the JSON Schema of CommentParams.
var commentSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["issue", "body"],
  "properties": {
    "issue": {"type": "string", "pattern": %q, "maxLength": %d, "description": "The issue to comment on: its key, such as OPS-123."},
    "body": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The comment, as plain text."}
  }
}`, issueRefSchema, maxRefChars, maxCommentChars))

// CommentConfig is the section of one jira.comment action.
type CommentConfig struct {
	Connection `yaml:",inline"`
	Limits     CommentLimits `yaml:"limits"`
}

// CommentLimits say which issues may get a comment.
type CommentLimits struct {
	// Projects are the keys, such as OPS, of the projects whose issues may
	// get a comment.
	Projects []string `yaml:"projects"`
}

// Validate checks the section without secrets or network calls.
func (c *CommentConfig) Validate() error {
	return errors.Join(append(c.validate(), validateProjects(c.Limits.Projects)...)...)
}

// CommentParams are what the model fills in for jira.comment.
type CommentParams struct {
	Issue string `json:"issue"`
	Body  string `json:"body"`
}

// Comment is one configured jira.comment action.
type Comment struct {
	service
}

var _ action.Action = (*Comment)(nil)

// NewComment builds a jira.comment action. It does not call Jira.
func NewComment(_ context.Context, name string, cfg CommentConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, cfg.Limits.Projects, deps)
	if err != nil {
		return nil, err
	}

	return &Comment{service: svc}, nil
}

// Spec implements action.Action.
func (c *Comment) Spec() action.Spec {
	return action.Spec{
		Name: c.name,
		Description: "Adds a comment to a Jira issue. Allowed projects: " + strings.Join(c.projects, ", ") +
			". The text is posted as plain text; formatting, links and mentions in it are not interpreted.",
		ParamsSchema: commentSchema,
	}
}

// Validate implements action.Action. It checks the form of the parameters
// and, when the issue is given by key, that its project is allowed; Execute
// settles an issue given by ID with the project Jira returns.
func (c *Comment) Validate(_ context.Context, raw json.RawMessage) error {
	_, err := c.check(raw)

	return err
}

// Execute implements action.Action. It reads the issue, checks its project,
// and looks through the newest comments for the one an earlier attempt
// posted, by the marker line, before it posts one.
func (c *Comment) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := c.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	issue, err := c.client.issue(ctx, p.Issue)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the issue %s: %w", p.Issue, err)
	}

	if !projectAllowed(c.projects, issue.Project) {
		return action.Result{}, limitError(errProject, "issue %s is in project %s", issue.Key, issue.Project)
	}

	marker := markerPrefix + keyHash(inv.IdempotencyKey)

	id, err := c.client.findComment(ctx, issue.ID, marker)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if id == "" {
		id, err = c.client.createComment(ctx, issue.ID, document(p.Body, c.investigationLink(inv.InvestigationID), marker))
		if err != nil {
			return action.Result{}, fmt.Errorf("comment on %s: %w", issue.Key, err)
		}
	}

	return action.Result{
		Summary: "Commented on " + issue.Key + ".",
		Links:   []action.Link{{Title: issue.Key + " comment", URL: c.client.browseURL(issue.Key) + "?focusedCommentId=" + id}},
	}, nil
}

// check decodes the parameters and checks them against the limits.
func (c *Comment) check(raw json.RawMessage) (CommentParams, error) {
	var p CommentParams
	if err := decodeStrict(raw, &p); err != nil {
		return CommentParams{}, err
	}

	if m := issueKeyPattern.FindStringSubmatch(p.Issue); m != nil {
		if !projectAllowed(c.projects, m[1]) {
			return CommentParams{}, limitError(errProject, "issue %s", p.Issue)
		}
	} else if !issueIDPattern.MatchString(p.Issue) {
		return CommentParams{}, errBadIssue
	}

	switch n := utf8.RuneCountInString(p.Body); {
	case strings.TrimSpace(p.Body) == "":
		return CommentParams{}, errEmptyBody
	case n > maxCommentChars:
		return CommentParams{}, limitError(errLongBody, "%d characters, at most %d", n, maxCommentChars)
	}

	return p, nil
}

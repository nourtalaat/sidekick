package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// issueRefSchema is a regular expression for the issue of a comment: an
// identifier such as OPS-123, or an issue ID.
const issueRefSchema = `^([A-Za-z][A-Za-z0-9_]{0,19}-[0-9]{1,9}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`

// commentSchema is the JSON Schema of CommentParams. Sidekick checks the
// parameters against it before it calls Validate, and the model reads it.
var commentSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["issue", "body"],
  "properties": {
    "issue": {"type": "string", "pattern": %q, "maxLength": %d, "description": "The issue to comment on: its identifier, such as OPS-123."},
    "body": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The comment, as plain text."}
  }
}`, issueRefSchema, maxRefChars, maxCommentChars))

var issueRefPattern = regexp.MustCompile(issueRefSchema)

var (
	errBadIssue  = errors.New("issue: expected an identifier such as OPS-123, or an issue ID")
	errEmptyBody = errors.New("body: required")
	errLongBody  = errors.New("body: too long")
	errTeamLimit = errors.New("the issue is not in one of limits.teams")
)

// CommentConfig is the section of one linear.comment action.
type CommentConfig struct {
	Connection `yaml:",inline"`
	Limits     CommentLimits `yaml:"limits"`
}

// CommentLimits say which issues may get a comment.
type CommentLimits struct {
	// Teams are the keys, such as OPS, or the IDs of the teams whose issues
	// may get a comment.
	Teams []string `yaml:"teams"`
}

// Validate checks the section without secrets or network calls.
func (c *CommentConfig) Validate() error {
	errs := c.validate()
	errs = append(errs, validateTeams(c.Limits.Teams)...)

	return errors.Join(errs...)
}

// CommentParams are what the model fills in for linear.comment.
type CommentParams struct {
	Issue string `json:"issue"`
	Body  string `json:"body"`
}

// Comment is one configured linear.comment action.
type Comment struct {
	service

	teams []string
}

var _ action.Action = (*Comment)(nil)

// NewComment builds a linear.comment action. It does not call Linear.
func NewComment(_ context.Context, name string, cfg CommentConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, deps)
	if err != nil {
		return nil, fmt.Errorf("linear action %q: %w", name, err)
	}

	return &Comment{service: svc, teams: cfg.Limits.Teams}, nil
}

// Spec implements action.Action.
func (c *Comment) Spec() action.Spec {
	return action.Spec{
		Name: c.name,
		Description: "Adds a comment to a Linear issue. Allowed teams: " + strings.Join(c.teams, ", ") +
			". The text is posted as plain text; formatting, links and mentions in it are not interpreted.",
		ParamsSchema: commentSchema,
	}
}

// Validate implements action.Action. It checks the form of the parameters
// and, when the issue's identifier names a team, that the team is allowed;
// Execute settles an issue given by ID with the team Linear returns.
func (c *Comment) Validate(_ context.Context, raw json.RawMessage) error {
	if _, err := c.check(raw); err != nil {
		return fmt.Errorf("validate %s: %w", c.name, err)
	}

	return nil
}

// Execute implements action.Action. A repeat with the same idempotency key
// finds the comment the first attempt made, by the ID that is made from the
// key, and makes no second one.
func (c *Comment) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := c.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	id := deriveUUID(inv.IdempotencyKey)

	made, err := c.client.comment(ctx, id)

	switch {
	case err == nil:
		return commentResult(p.Issue, made, ""), nil
	case !errors.Is(err, errNotFound):
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	issue, err := c.client.issue(ctx, p.Issue)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the issue %s: %w", p.Issue, err)
	}

	if !teamAllowed(c.teams, issue.Team.Key, issue.Team.ID) {
		return action.Result{}, action.LimitError(errTeamLimit, "issue %s is in team %s", issue.Identifier, issue.Team.Key)
	}

	body := withFooter(p.Body, inv, c.publicURL)

	made, err = c.client.createComment(ctx, id, issue.ID, body)
	if err != nil {
		// The comment may exist although the answer did not arrive.
		if again, lookupErr := c.client.comment(ctx, id); lookupErr == nil {
			return commentResult(issue.Identifier, again, issue.URL), nil
		}

		return action.Result{}, fmt.Errorf("comment on %s: %w", issue.Identifier, err)
	}

	return commentResult(issue.Identifier, made, issue.URL), nil
}

// check decodes the parameters and checks them against the limits.
func (c *Comment) check(raw json.RawMessage) (CommentParams, error) {
	var p CommentParams
	if err := action.DecodeParams(raw, &p); err != nil {
		return CommentParams{}, fmt.Errorf("decode: %w", err)
	}

	if !issueRefPattern.MatchString(p.Issue) {
		return CommentParams{}, errBadIssue
	}

	if team, _, ok := strings.Cut(p.Issue, "-"); ok && !isUUID(p.Issue) && !teamMayBeAllowed(c.teams, team) {
		return CommentParams{}, action.LimitError(errTeamLimit, "issue %s", p.Issue)
	}

	switch n := utf8.RuneCountInString(p.Body); {
	case strings.TrimSpace(p.Body) == "":
		return CommentParams{}, errEmptyBody
	case n > maxCommentChars:
		return CommentParams{}, action.LimitError(errLongBody, "%d characters, at most %d", n, maxCommentChars)
	}

	return p, nil
}

// commentResult describes a comment that exists. The link goes to the
// comment, or to the issue when Linear gave no address for the comment.
func commentResult(identifier string, made commentRef, issueURL string) action.Result {
	url := made.URL
	if url == "" {
		url = issueURL
	}

	res := action.Result{Summary: "Commented on " + identifier + "."}
	if url != "" {
		res.Links = []action.Link{{Title: identifier + " comment", URL: url}}
	}

	return res
}

package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/daily"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var (
	errNoTeam     = errors.New("team: required")
	errNoTitle    = errors.New("title: required")
	errLongTitle  = errors.New("title: too long")
	errLongText   = errors.New("description: too long")
	errManyLabels = errors.New("labels: too many")
	errLabelLimit = errors.New("the label is not one of limits.labels")
	errBadLabel   = errors.New("labels: a label must not be empty or too long")
	errIssueTeam  = errors.New("the team is not one of limits.teams")
)

// IssueConfig is the section of one linear.issue action.
type IssueConfig struct {
	Connection `yaml:",inline"`
	Limits     IssueLimits `yaml:"limits"`
}

// IssueLimits say which issues the action may create, and how many.
type IssueLimits struct {
	// Teams are the keys, such as OPS, or the IDs of the teams that may get
	// an issue.
	Teams []string `yaml:"teams"`
	// Labels are the labels an issue may get. Empty means none.
	Labels []string `yaml:"labels"`
	// MaxPerDay is the most issues the action creates in a day of
	// budgets.daily.timeZone.
	MaxPerDay int `yaml:"maxPerDay"`
}

// Validate checks the section without secrets or network calls.
func (c *IssueConfig) Validate() error {
	errs := c.validate()
	errs = append(errs, validateTeams(c.Limits.Teams)...)

	for i, label := range c.Limits.Labels {
		if strings.TrimSpace(label) == "" || utf8.RuneCountInString(label) > maxLabelChars {
			errs = append(errs, fmt.Errorf("limits.labels[%d]: must not be empty or longer than %d characters", i, maxLabelChars))
		}
	}

	if c.Limits.MaxPerDay < 1 {
		errs = append(errs, errMaxPerDay)
	}

	return errors.Join(errs...)
}

// IssueParams are what the model fills in for linear.issue.
type IssueParams struct {
	Team        string   `json:"team"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// Issue is one configured linear.issue action.
type Issue struct {
	service

	limits  IssueLimits
	counter daily.Limiter
	schema  json.RawMessage
}

var _ action.Action = (*Issue)(nil)

// NewIssue builds a linear.issue action. It does not call Linear.
func NewIssue(_ context.Context, name string, cfg IssueConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, deps)
	if err != nil {
		return nil, err
	}

	return &Issue{
		service: svc,
		limits:  cfg.Limits,
		counter: daily.Counter{DB: deps.DB, Location: deps.DailyLocation},
		schema:  issueSchema(),
	}, nil
}

// Spec implements action.Action.
func (i *Issue) Spec() action.Spec {
	labels := "none"
	if len(i.limits.Labels) > 0 {
		labels = strings.Join(i.limits.Labels, ", ")
	}

	return action.Spec{
		Name: i.name,
		Description: fmt.Sprintf("Creates a Linear issue. Allowed teams: %s. Allowed labels: %s. At most %d issues a day. "+
			"The text is posted as plain text; formatting, links and mentions in it are not interpreted.",
			strings.Join(i.limits.Teams, ", "), labels, i.limits.MaxPerDay),
		ParamsSchema: i.schema,
	}
}

// Validate implements action.Action. It checks the form of the parameters,
// the team and the labels against the limits. The daily limit is checked
// when the issue is created.
func (i *Issue) Validate(_ context.Context, raw json.RawMessage) error {
	_, err := i.check(raw)

	return err
}

// Execute implements action.Action. A repeat with the same idempotency key
// finds the issue the first attempt made, by the ID that is made from the
// key or, failing that, by the key in the description, and makes no second
// one. The daily limit is checked only when the issue does not exist yet.
func (i *Issue) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := i.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	id := deriveUUID(inv.IdempotencyKey)

	found, err := i.client.issue(ctx, id)

	switch {
	case err == nil:
		return issueResult(found), nil
	case !errors.Is(err, errNotFound):
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	team, err := i.client.team(ctx, p.Team)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the team %s: %w", p.Team, err)
	}

	if !teamAllowed(i.limits.Teams, team.Key, team.ID) {
		return action.Result{}, limitError(errIssueTeam, "team %s", team.Key)
	}

	found, err = i.client.issueByMarker(ctx, team.ID, markerOf(inv.IdempotencyKey))

	switch {
	case err == nil:
		return issueResult(found), nil
	case !errors.Is(err, errNotFound):
		return action.Result{}, fmt.Errorf("look for an earlier attempt by its key: %w", err)
	}

	if err := i.counter.Check(ctx, i.name, inv.ProposalID, i.limits.MaxPerDay); err != nil {
		return action.Result{}, fmt.Errorf("check the daily limit: %w", err)
	}

	labelIDs, err := i.client.labelIDs(ctx, p.Labels, team.ID)
	if err != nil {
		return action.Result{}, fmt.Errorf("find the labels: %w", err)
	}

	created, err := i.client.createIssue(ctx, issueInput{
		ID:          id,
		TeamID:      team.ID,
		Title:       p.Title,
		Description: issueDescription(p.Description, inv.IdempotencyKey, i.publicURL, inv.InvestigationID),
		LabelIDs:    labelIDs,
	})
	if err != nil {
		// The issue may exist although the answer did not arrive.
		if again, lookupErr := i.client.issue(ctx, id); lookupErr == nil {
			return issueResult(again), nil
		}

		return action.Result{}, fmt.Errorf("create the issue in team %s: %w", team.Key, err)
	}

	return issueResult(created), nil
}

// check decodes the parameters and checks them against the limits.
func (i *Issue) check(raw json.RawMessage) (IssueParams, error) {
	var p IssueParams
	if err := decodeStrict(raw, &p); err != nil {
		return IssueParams{}, err
	}

	switch n := utf8.RuneCountInString(p.Title); {
	case strings.TrimSpace(p.Title) == "":
		return IssueParams{}, errNoTitle
	case n > maxTitleChars:
		return IssueParams{}, limitError(errLongTitle, "%d characters, at most %d", n, maxTitleChars)
	}

	if n := utf8.RuneCountInString(p.Description); n > maxDescriptionChars {
		return IssueParams{}, limitError(errLongText, "%d characters, at most %d", n, maxDescriptionChars)
	}

	if p.Team == "" {
		return IssueParams{}, errNoTeam
	}

	if !teamMayBeAllowed(i.limits.Teams, p.Team) || (!isUUID(p.Team) && !teamPattern.MatchString(p.Team)) {
		return IssueParams{}, limitError(errIssueTeam, "team %q", p.Team)
	}

	if err := i.checkLabels(p.Labels); err != nil {
		return IssueParams{}, err
	}

	return p, nil
}

// checkLabels checks the labels against limits.labels.
func (i *Issue) checkLabels(labels []string) error {
	if len(labels) > maxLabels {
		return limitError(errManyLabels, "%d labels, at most %d", len(labels), maxLabels)
	}

	for _, label := range labels {
		if strings.TrimSpace(label) == "" || utf8.RuneCountInString(label) > maxLabelChars {
			return errBadLabel
		}

		allowed := false

		for _, a := range i.limits.Labels {
			if strings.EqualFold(a, label) {
				allowed = true

				break
			}
		}

		if !allowed {
			return limitError(errLabelLimit, "label %q", label)
		}
	}

	return nil
}

// issueSchema returns the JSON Schema of IssueParams.
func issueSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["team", "title"],
  "properties": {
    "team": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The key of the team, such as OPS."},
    "title": {"type": "string", "minLength": 1, "maxLength": %d},
    "description": {"type": "string", "maxLength": %d, "description": "The description, as plain text."},
    "labels": {"type": "array", "maxItems": %d, "items": {"type": "string", "minLength": 1, "maxLength": %d}}
  }
}`, maxRefChars, maxTitleChars, maxDescriptionChars, maxLabels, maxLabelChars))
}

// issueResult describes an issue that exists.
func issueResult(issue issueRef) action.Result {
	res := action.Result{Summary: "Created the Linear issue " + issue.Identifier + "."}
	if issue.URL != "" {
		res.Links = []action.Link{{Title: issue.Identifier, URL: issue.URL}}
	}

	return res
}

// keyPrefix starts the line of a description that holds the idempotency key.
const keyPrefix = "Sidekick key: "

// markerOf is the line of an issue description that holds the idempotency
// key, so the issue can be found again.
func markerOf(key string) string {
	return keyPrefix + key
}

// issueDescription returns the description of an issue: the model's text,
// escaped, then the footer and the line with the idempotency key. The key
// is made from the proposal ID, never from the model's text.
func issueDescription(text, key, publicURL, investigationID string) string {
	return withFooter(text, publicURL, investigationID) + "\n\n" + markerOf(key)
}

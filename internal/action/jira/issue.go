package jira

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/daily"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// labelPrefix starts the label that marks an issue Sidekick created. The
// rest is a short hash of the idempotency key, so a repeat can find the issue.
const labelPrefix = "sidekick-"

var (
	errNoSummary   = errors.New("summary: required")
	errLongSummary = errors.New("summary: too long")
	errLongText    = errors.New("description: too long")
	errBadProject  = errors.New("project: expected a project key such as OPS")
	errProjectLim  = errors.New("the project is not one of limits.projects")
	errTypeLimit   = errors.New("the issue type is not one of limits.issueTypes")
	errLabelLimit  = errors.New("the label is not one of limits.labels")
	errManyLabels  = errors.New("labels: too many")
	errBadLabel    = errors.New("labels: a label must have no spaces")
)

// IssueConfig is the section of one jira.issue action.
type IssueConfig struct {
	Connection `yaml:",inline"`
	Limits     IssueLimits `yaml:"limits"`
}

// IssueLimits say which issues the action may create, and how many.
type IssueLimits struct {
	// Projects are the keys, such as OPS, of the projects that may get an issue.
	Projects []string `yaml:"projects"`
	// IssueTypes are the names of the issue types it may create, such as Task.
	IssueTypes []string `yaml:"issueTypes"`
	// Labels are the labels an issue may get. Empty means none.
	Labels []string `yaml:"labels"`
	// MaxPerDay is the most issues the action creates in a day of
	// budgets.daily.timeZone.
	MaxPerDay int `yaml:"maxPerDay"`
}

// Validate checks the section without secrets or network calls.
func (c *IssueConfig) Validate() error {
	errs := append(c.validate(), validateProjects(c.Limits.Projects)...)
	errs = append(errs, validateLabels(c.Limits.Labels)...)

	if len(c.Limits.IssueTypes) == 0 {
		errs = append(errs, errNoTypes)
	}

	for i, name := range c.Limits.IssueTypes {
		if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > maxTypeChars {
			errs = append(errs, fmt.Errorf("limits.issueTypes[%d]: must not be empty or longer than %d characters", i, maxTypeChars))
		}
	}

	if c.Limits.MaxPerDay < 1 {
		errs = append(errs, errMaxPerDay)
	}

	return errors.Join(errs...)
}

// IssueParams are what the model fills in for jira.issue.
type IssueParams struct {
	Project     string   `json:"project"`
	IssueType   string   `json:"issueType"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

// Issue is one configured jira.issue action.
type Issue struct {
	service

	limits  IssueLimits
	counter daily.Limiter
}

var _ action.Action = (*Issue)(nil)

// NewIssue builds a jira.issue action. It does not call Jira.
func NewIssue(_ context.Context, name string, cfg IssueConfig, deps registry.Deps) (action.Action, error) {
	svc, err := newService(name, cfg.Connection, cfg.Limits.Projects, deps)
	if err != nil {
		return nil, err
	}

	return &Issue{service: svc, limits: cfg.Limits, counter: daily.Counter{DB: deps.DB, Location: deps.DailyLocation}}, nil
}

// Spec implements action.Action.
func (i *Issue) Spec() action.Spec {
	labels := "none"
	if len(i.limits.Labels) > 0 {
		labels = strings.Join(i.limits.Labels, ", ")
	}

	return action.Spec{
		Name: i.name,
		Description: fmt.Sprintf("Creates a Jira issue. Allowed projects: %s. Allowed issue types: %s. Allowed labels: %s. At most %d issues a day. "+
			"The text is posted as plain text; formatting, links and mentions in it are not interpreted.",
			strings.Join(i.limits.Projects, ", "), strings.Join(i.limits.IssueTypes, ", "), labels, i.limits.MaxPerDay),
		ParamsSchema: i.schema(),
	}
}

// Validate implements action.Action. The daily limit is checked when the
// issue is created.
func (i *Issue) Validate(_ context.Context, raw json.RawMessage) error {
	_, err := i.check(raw)

	return err
}

// Execute implements action.Action. A repeat with the same idempotency key
// finds the issue the first attempt made, by the label made from the key,
// and makes no second one; it is then not counted against the daily limit.
// Jira's search can lag by a few seconds, so a repeat right after a lost
// answer may not see the issue yet.
func (i *Issue) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	p, err := i.check(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: validate before execute: %w", action.ErrPermanent, err)
	}

	label := labelPrefix + keyHash(inv.IdempotencyKey)

	found, err := i.client.findIssue(ctx, p.Project, label)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for an earlier attempt: %w", err)
	}

	if found.Key != "" {
		return i.result(found), nil
	}

	if err := i.counter.Check(ctx, i.name, inv.ProposalID, i.limits.MaxPerDay); err != nil {
		return action.Result{}, fmt.Errorf("check the daily limit: %w", err)
	}

	created, err := i.client.createIssue(ctx, issueFields{
		Project:     keyed{Key: p.Project},
		IssueType:   named{Name: p.IssueType},
		Summary:     oneLine(p.Summary),
		Description: document(p.Description, i.investigationLink(inv.InvestigationID), ""),
		Labels:      append(slices.Clone(p.Labels), label),
	})
	if err != nil {
		return action.Result{}, fmt.Errorf("create the issue in project %s: %w", p.Project, err)
	}

	return i.result(created), nil
}

// result describes an issue that exists.
func (i *Issue) result(issue issueRef) action.Result {
	return action.Result{
		Summary: "Created the Jira issue " + issue.Key + ".",
		Links:   []action.Link{{Title: issue.Key, URL: i.client.browseURL(issue.Key)}},
	}
}

// check decodes the parameters and checks them against the limits. The
// issue type it returns is spelled as the config spells it.
func (i *Issue) check(raw json.RawMessage) (IssueParams, error) {
	var p IssueParams
	if err := decodeStrict(raw, &p); err != nil {
		return IssueParams{}, err
	}

	if !projectPattern.MatchString(p.Project) {
		return IssueParams{}, errBadProject
	}

	if !projectAllowed(i.limits.Projects, p.Project) {
		return IssueParams{}, limitError(errProjectLim, "project %q", p.Project)
	}

	idx := slices.IndexFunc(i.limits.IssueTypes, func(t string) bool { return strings.EqualFold(t, p.IssueType) })
	if idx < 0 {
		return IssueParams{}, limitError(errTypeLimit, "issue type %q", p.IssueType)
	}

	p.IssueType = i.limits.IssueTypes[idx]

	summary := oneLine(p.Summary)

	switch n := utf8.RuneCountInString(summary); {
	case summary == "":
		return IssueParams{}, errNoSummary
	case n > maxSummaryChars:
		return IssueParams{}, limitError(errLongSummary, "%d characters, at most %d", n, maxSummaryChars)
	}

	if n := utf8.RuneCountInString(p.Description); n > maxDescriptionChars {
		return IssueParams{}, limitError(errLongText, "%d characters, at most %d", n, maxDescriptionChars)
	}

	if err := i.checkLabels(p.Labels); err != nil {
		return IssueParams{}, err
	}

	return p, nil
}

// checkLabels checks the labels against limits.labels. Jira labels are case
// sensitive, so the match is exact.
func (i *Issue) checkLabels(labels []string) error {
	if len(labels) > maxLabels {
		return limitError(errManyLabels, "%d labels, at most %d", len(labels), maxLabels)
	}

	for _, l := range labels {
		if !labelPattern.MatchString(l) {
			return errBadLabel
		}

		if !slices.Contains(i.limits.Labels, l) {
			return limitError(errLabelLimit, "label %q", l)
		}
	}

	return nil
}

// schema returns the JSON Schema of IssueParams, with the allowed issue
// types as an enum.
func (i *Issue) schema() json.RawMessage {
	types, err := json.Marshal(i.limits.IssueTypes)
	if err != nil {
		types = []byte("[]")
	}

	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["project", "issueType", "summary"],
  "properties": {
    "project": {"type": "string", "minLength": 1, "maxLength": %d, "description": "The key of the project, such as OPS."},
    "issueType": {"type": "string", "enum": %s},
    "summary": {"type": "string", "minLength": 1, "maxLength": %d, "description": "One line."},
    "description": {"type": "string", "maxLength": %d, "description": "The description, as plain text."},
    "labels": {"type": "array", "maxItems": %d, "items": {"type": "string", "minLength": 1, "maxLength": %d}}
  }
}`, maxRefChars, types, maxSummaryChars, maxDescriptionChars, maxLabels, maxLabelChars))
}

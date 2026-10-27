// Package jira is the Jira output: it posts the result of an investigation
// as a comment on the related Jira issue, through the REST API version 3 of
// Jira Cloud with an API token. Finding the issue, which phases it shows
// and when it edits are in package ticket; this package talks to Jira and
// writes the comment in the Atlassian Document Format, where text is
// always plain text, so untrusted text cannot add formatting, links or
// mentions.
package jira

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the output type this package registers. A config selects it with
// "type: jira.comment".
const Type = "jira.comment"

// defaultLabel is the event label that names the related issue when the
// config lists none.
const defaultLabel = "jira_issue"

// projectPattern is what a project key looks like.
var projectPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)

var (
	errNoURL      = errors.New("url: required, the address of the Jira site, such as https://acme.atlassian.net")
	errNoEmail    = errors.New("email: required, the email address of the account the API token belongs to")
	errBadEmail   = errors.New("email: expected an email address")
	errNoToken    = errors.New("apiTokenSecret: required")
	errNoProjects = errors.New("projects: list at least one project key, such as OPS; only issues in these projects get a comment")
	errNoSecrets  = errors.New("no secrets resolver to read the API token with")
)

// init registers the output type when the package is linked in.
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one Jira output.
type Config struct {
	// URL is the address of the Jira Cloud site, such as
	// https://acme.atlassian.net. Issue links of other sites are ignored.
	URL string `yaml:"url"`
	// Email is the email address of the account the API token belongs to.
	Email string `yaml:"email"`
	// APITokenSecret holds an API token of that account that can browse
	// the projects below and add comments in them.
	APITokenSecret registry.SecretRef `yaml:"apiTokenSecret"`
	// Projects are the keys of the projects whose issues may get a
	// comment, such as OPS.
	Projects []string `yaml:"projects"`
	// Labels are the event labels that may hold the related issue, as a
	// key such as OPS-123 or a link, in order. Empty means [jira_issue].
	Labels []string `yaml:"labels"`
}

// Validate checks the config without secrets or network calls.
func (c *Config) Validate() error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errNoURL)
	} else {
		errs = append(errs, sinkhttp.CheckURL("url", c.URL))
	}

	if c.Email == "" {
		errs = append(errs, errNoEmail)
	} else if addr, err := mail.ParseAddress(c.Email); err != nil || addr.Address != c.Email {
		errs = append(errs, errBadEmail)
	}

	if c.APITokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errNoToken)
	}

	if len(c.Projects) == 0 {
		errs = append(errs, errNoProjects)
	}

	for i, project := range c.Projects {
		if !projectPattern.MatchString(project) {
			errs = append(errs, fmt.Errorf("projects[%d]: %q is not a project key, such as OPS", i, project))
		}
	}

	for i, label := range c.Labels {
		if strings.TrimSpace(label) == "" {
			errs = append(errs, fmt.Errorf("labels[%d]: must not be empty", i))
		}
	}

	return errors.Join(errs...)
}

// New builds a Jira output. It reads the API token through deps.Secrets on
// every call and sends requests with deps.HTTP, or a client from
// httpclient.New when it is nil.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	if deps.Secrets == nil {
		return nil, fmt.Errorf("jira output %q: %w", name, errNoSecrets)
	}

	hc, err := sinkhttp.New(Type, deps.HTTP, deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("jira output %q: %w", name, err)
	}

	token := func(ctx context.Context) (string, error) {
		t, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APITokenSecret)
		if err != nil {
			return "", fmt.Errorf("read apiTokenSecret: %w", err)
		}

		return t, nil
	}

	c, err := newClient(cfg.URL, cfg.Email, hc, token)
	if err != nil {
		return nil, fmt.Errorf("jira output %q: %w", name, err)
	}

	labels := cfg.Labels
	if len(labels) == 0 {
		labels = []string{defaultLabel}
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return ticket.NewSink(ticket.Options{Name: name, Tracker: c, Labels: labels, Scopes: cfg.Projects, Logger: log}), nil
}

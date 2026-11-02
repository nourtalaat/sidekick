// Package jira holds the actions that comment on a Jira issue (jira.comment)
// and create one (jira.issue), through the REST API version 3 of Jira Cloud
// with an email and an API token. The model's text goes into the Atlassian
// Document Format as plain text nodes, so it cannot add formatting, links or
// mentions.
package jira

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// The action types this package registers. A config selects one with
// "type: jira.comment" or "type: jira.issue".
const (
	TypeComment = "jira.comment"
	TypeIssue   = "jira.issue"
)

// Limits on the parameters the model may give.
const (
	maxCommentChars     = 4000
	maxSummaryChars     = 200
	maxDescriptionChars = 4000
	maxLabels           = 5
	maxLabelChars       = 100
	maxRefChars         = 40
	maxTypeChars        = 60
)

// keyHexChars is how many hex characters of the SHA-256 sum of the
// idempotency key the marker of a comment, and the label of an issue, hold.
const keyHexChars = 16

var (
	// projectPattern is what a project key looks like, such as OPS.
	projectPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)
	// issueKeyPattern is what the key of an issue looks like, such as OPS-123.
	issueKeyPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]{0,19})-[0-9]{1,9}$`)
	// issueIDPattern is what the numeric ID of an issue looks like.
	issueIDPattern = regexp.MustCompile(`^[0-9]{1,18}$`)
	// labelPattern is what a Jira label looks like: no spaces.
	labelPattern = regexp.MustCompile(`^[^\s]{1,100}$`)
)

var (
	errNoURL      = errors.New("url: required, the address of the Jira Cloud site, such as https://acme.atlassian.net")
	errNoEmail    = errors.New("email: required, the email address of the account the API token belongs to")
	errBadEmail   = errors.New("email: expected an email address")
	errNoToken    = errors.New("apiTokenSecret: required")
	errBearer     = errors.New("bearerTokenSecret: Jira Data Center is not supported by this action yet; use url, email and apiTokenSecret with a Jira Cloud site")
	errNoProjects = errors.New("limits.projects: list at least one project key, such as OPS")
	errNoSecrets  = errors.New("no secrets resolver to read the API token with")
	errMaxPerDay  = errors.New("limits.maxPerDay: required, at least 1")
	errNoTypes    = errors.New("limits.issueTypes: list at least one issue type, such as Task")
)

// init registers the action types when the package is linked in.
func init() {
	registry.Actions.Register(TypeComment, registry.Typed(NewComment))
	registry.Actions.Register(TypeIssue, registry.Typed(NewIssue))
}

// Connection is how an action reaches Jira.
type Connection struct {
	// URL is the address of the Jira Cloud site, such as
	// https://acme.atlassian.net. It must be https, except on localhost.
	URL string `yaml:"url"`
	// Email is the email address of the account the API token belongs to.
	Email string `yaml:"email"`
	// APITokenSecret holds an API token of that account. Give the action an
	// account and a token of its own, apart from any the Jira output uses,
	// that can browse and comment on the projects below, and for
	// jira.issue create issues in them.
	APITokenSecret registry.SecretRef `yaml:"apiTokenSecret"`
	// BearerTokenSecret is for Jira Data Center, which these actions do not
	// support yet. Setting it is an error that says so.
	BearerTokenSecret registry.SecretRef `yaml:"bearerTokenSecret"`
}

// validate checks the connection without secrets or network calls.
func (c Connection) validate() []error {
	var errs []error

	if c.URL == "" {
		errs = append(errs, errNoURL)
	} else {
		errs = append(errs, actionhttp.CheckURL("url", c.URL))
	}

	if c.Email == "" {
		errs = append(errs, errNoEmail)
	} else if addr, err := mail.ParseAddress(c.Email); err != nil || addr.Address != c.Email {
		errs = append(errs, errBadEmail)
	}

	if c.APITokenSecret == (registry.SecretRef{}) {
		errs = append(errs, errNoToken)
	}

	if c.BearerTokenSecret != (registry.SecretRef{}) {
		errs = append(errs, errBearer)
	}

	return errs
}

// validateProjects checks the allowed projects.
func validateProjects(projects []string) []error {
	if len(projects) == 0 {
		return []error{errNoProjects}
	}

	var errs []error

	for i, p := range projects {
		if !projectPattern.MatchString(p) {
			errs = append(errs, fmt.Errorf("limits.projects[%d]: %q is not a project key, such as OPS", i, p))
		}
	}

	return errs
}

// validateLabels checks a list of allowed labels.
func validateLabels(labels []string) []error {
	var errs []error

	for i, l := range labels {
		if !labelPattern.MatchString(l) {
			errs = append(errs, fmt.Errorf("limits.labels[%d]: %q is not a label: it must have no spaces and at most %d characters", i, l, maxLabelChars))
		}
	}

	return errs
}

// service is what both action types share.
type service struct {
	name      string
	projects  []string
	client    *client
	publicURL string
}

// newService builds the part both actions share.
func newService(name string, conn Connection, projects []string, deps registry.Deps) (service, error) {
	if deps.Secrets == nil {
		return service{}, errNoSecrets
	}

	site, err := url.Parse(conn.URL)
	if err != nil || site.Host == "" {
		return service{}, errNoURL
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	return service{
		name:      name,
		projects:  projects,
		client:    &client{site: site, email: conn.Email, http: hc, secrets: deps.Secrets, token: conn.APITokenSecret},
		publicURL: deps.PublicURL,
	}, nil
}

// projectAllowed reports whether key is one of the allowed projects.
func projectAllowed(allowed []string, key string) bool {
	return slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, key) })
}

// keyHash returns the short hash of an idempotency key that names its
// marker and its label.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))

	return hex.EncodeToString(sum[:])[:keyHexChars]
}

// investigationLink returns the address of the investigation, or "" when
// there is no public URL.
func (s service) investigationLink(investigationID string) string {
	return weblink.Investigation(s.publicURL, investigationID)
}

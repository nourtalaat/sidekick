// Package linear is the Linear output: it posts the result of an
// investigation as a comment on the related Linear issue, through Linear's
// GraphQL API with an API key. Finding the issue, which phases it shows
// and when it edits are in package ticket; this package talks to Linear
// and writes the comment in Markdown with every untrusted character
// escaped.
package linear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/output/ticket"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Type is the output type this package registers. A config selects it with
// "type: linear.comment".
const Type = "linear.comment"

// DefaultAPIURL is Linear's GraphQL endpoint.
const DefaultAPIURL = "https://api.linear.app/graphql"

// defaultLabel is the event label that names the related issue when the
// config lists none.
const defaultLabel = "linear_issue"

// The patterns of a workspace URL key and a team key.
var (
	workspacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	teamPattern      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)
)

var (
	errNoAPIKey     = errors.New("apiKeySecret: required")
	errNoWorkspace  = errors.New(`workspace: required, the URL key of the workspace, as in https://linear.app/<workspace>/`)
	errBadWorkspace = errors.New(`workspace: expected the URL key of the workspace, lower-case letters, digits and "-", as in https://linear.app/<workspace>/`)
	errNoTeams      = errors.New("teams: list at least one team key, such as OPS; only issues in these teams get a comment")
	errNoSecrets    = errors.New("no secrets resolver to read the API key with")
)

// init registers the output type when the package is linked in.
func init() {
	registry.Outputs.Register(Type, registry.Typed(New))
}

// Config is the section of one Linear output.
type Config struct {
	// APIKeySecret holds a Linear API key that can read issues and create
	// comments in the teams below.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// Workspace is the URL key of the Linear workspace, as in
	// https://linear.app/<workspace>/. Issue links of other workspaces are
	// ignored.
	Workspace string `yaml:"workspace"`
	// Teams are the keys of the teams whose issues may get a comment, such
	// as OPS.
	Teams []string `yaml:"teams"`
	// Labels are the event labels that may hold the related issue, as an
	// identifier such as OPS-123 or a link, in order. Empty means
	// [linear_issue].
	Labels []string `yaml:"labels"`
	// APIURL is the GraphQL endpoint. Empty means DefaultAPIURL.
	APIURL string `yaml:"apiURL"`
}

// Validate checks the config without secrets or network calls.
func (c *Config) Validate() error {
	var errs []error

	if c.APIKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errNoAPIKey)
	}

	switch {
	case c.Workspace == "":
		errs = append(errs, errNoWorkspace)
	case !workspacePattern.MatchString(c.Workspace):
		errs = append(errs, errBadWorkspace)
	}

	if len(c.Teams) == 0 {
		errs = append(errs, errNoTeams)
	}

	for i, team := range c.Teams {
		if !teamPattern.MatchString(team) {
			errs = append(errs, fmt.Errorf("teams[%d]: %q is not a team key, such as OPS", i, team))
		}
	}

	for i, label := range c.Labels {
		if strings.TrimSpace(label) == "" {
			errs = append(errs, fmt.Errorf("labels[%d]: must not be empty", i))
		}
	}

	if c.APIURL != "" {
		errs = append(errs, sinkhttp.CheckURL("apiURL", c.APIURL))
	}

	return errors.Join(errs...)
}

// New builds a Linear output. It reads the API key through deps.Secrets on
// every call and sends requests with deps.HTTP, or a client from
// httpclient.New when it is nil.
func New(_ context.Context, name string, cfg Config, deps registry.Deps) (output.Sink, error) {
	if deps.Secrets == nil {
		return nil, fmt.Errorf("linear output %q: %w", name, errNoSecrets)
	}

	hc, err := sinkhttp.New(Type, deps.HTTP, deps.Metrics)
	if err != nil {
		return nil, fmt.Errorf("linear output %q: %w", name, err)
	}

	apiKey := func(ctx context.Context) (string, error) {
		key, err := secrets.ResolveText(ctx, deps.Secrets, cfg.APIKeySecret)
		if err != nil {
			return "", fmt.Errorf("read apiKeySecret: %w", err)
		}

		return key, nil
	}

	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}

	labels := cfg.Labels
	if len(labels) == 0 {
		labels = []string{defaultLabel}
	}

	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return ticket.NewSink(ticket.Options{
		Name:    name,
		Tracker: newClient(apiURL, cfg.Workspace, hc, apiKey),
		Labels:  labels,
		Scopes:  cfg.Teams,
		Logger:  log,
	}), nil
}

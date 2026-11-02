// Package linear holds the actions that comment on a Linear issue
// (linear.comment) and create one (linear.issue), through Linear's GraphQL
// API. The model's text is posted as plain text: it is escaped for Markdown
// so it cannot add formatting, links or mentions.
package linear

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actionhttp"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// The action types this package registers. A config selects one with
// "type: linear.comment" or "type: linear.issue".
const (
	TypeComment = "linear.comment"
)

// DefaultAPIURL is Linear's GraphQL endpoint.
const DefaultAPIURL = "https://api.linear.app/graphql"

// Limits on the parameters the model may give.
const (
	maxCommentChars     = 4000
	maxTitleChars       = 200
	maxDescriptionChars = 4000
	maxLabels           = 5
	maxLabelChars       = 100
	maxRefChars         = 100
)

// teamPattern is what a team key looks like, such as OPS.
var teamPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)

var (
	errNoAPIKey  = errors.New("apiKeySecret: required")
	errNoTeams   = errors.New("limits.teams: list at least one team key or ID, such as OPS")
	errNoSecrets = errors.New("no secrets resolver to read the API key with")
	errMaxPerDay = errors.New("limits.maxPerDay: required, at least 1")
)

// init registers the action types when the package is linked in.
func init() {
	registry.Actions.Register(TypeComment, registry.Typed(NewComment))
}

// Connection is how an action reaches Linear.
type Connection struct {
	// APIKeySecret holds a Linear personal API key, or an OAuth app token,
	// with the rights the action needs: read issues and create comments for
	// linear.comment, create issues for linear.issue. Give the action a
	// key of its own, apart from any key a source or an output uses.
	APIKeySecret registry.SecretRef `yaml:"apiKeySecret"`
	// APIURL is the GraphQL endpoint. Empty means DefaultAPIURL.
	APIURL string `yaml:"apiURL"`
}

// validate checks the connection without secrets or network calls.
func (c Connection) validate() []error {
	var errs []error

	if c.APIKeySecret == (registry.SecretRef{}) {
		errs = append(errs, errNoAPIKey)
	}

	if c.APIURL != "" {
		errs = append(errs, actionhttp.CheckURL("apiURL", c.APIURL))
	}

	return errs
}

// validateTeams checks the allowed teams: team keys or team IDs.
func validateTeams(teams []string) []error {
	if len(teams) == 0 {
		return []error{errNoTeams}
	}

	var errs []error

	for i, team := range teams {
		if !teamPattern.MatchString(team) && !isUUID(team) {
			errs = append(errs, fmt.Errorf("limits.teams[%d]: %q is not a team key, such as OPS, or a team ID", i, team))
		}
	}

	return errs
}

// service is what both action types share: the client and where links to
// the investigation point.
type service struct {
	name      string
	client    *client
	publicURL string
}

// newService builds the part both actions share.
func newService(name string, conn Connection, deps registry.Deps) (service, error) {
	if deps.Secrets == nil {
		return service{}, fmt.Errorf("linear action %q: %w", name, errNoSecrets)
	}

	hc := deps.HTTP
	if hc == nil {
		hc = httpclient.New()
	}

	apiURL := conn.APIURL
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}

	return service{
		name:      name,
		client:    &client{apiURL: apiURL, http: hc, secrets: deps.Secrets, key: conn.APIKeySecret},
		publicURL: deps.PublicURL,
	}, nil
}

// teamAllowed reports whether a team with this key and ID is one of the
// allowed teams.
func teamAllowed(allowed []string, key, id string) bool {
	return slices.ContainsFunc(allowed, func(a string) bool {
		return strings.EqualFold(a, key) || strings.EqualFold(a, id)
	})
}

// teamMayBeAllowed reports whether ref, a team key or ID the model gave,
// can be one of the allowed teams. An entry of another kind than ref
// might name the same team, which only Linear can tell, so it counts as
// a maybe; Execute settles it with the team Linear returns.
func teamMayBeAllowed(allowed []string, ref string) bool {
	refIsID := isUUID(ref)

	return slices.ContainsFunc(allowed, func(a string) bool {
		return isUUID(a) != refIsID || strings.EqualFold(a, ref)
	})
}

// limitError returns an error that says what is outside the limits, and
// wraps action.ErrLimit and cause.
func limitError(cause error, format string, args ...any) error {
	return fmt.Errorf("%s: %w: %w", fmt.Sprintf(format, args...), action.ErrLimit, cause)
}

// isUUID reports whether s is a UUID in the usual 36 character form.
func isUUID(s string) bool {
	if len(s) != len(uuid.Nil.String()) {
		return false
	}

	_, err := uuid.Parse(s)

	return err == nil
}

// decodeStrict decodes the parameters of an action into v and refuses keys
// that v does not have.
func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode the parameters: %w", err)
	}

	return nil
}

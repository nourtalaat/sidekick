// Package slackroles finds the Sidekick role of a Slack user from
// slack.roleUsers and slack.roleGroups. Group membership is read from
// Slack with usergroups.users.list and kept in the slack_identities table
// for a day, so most lookups make no Slack call. A user neither map names
// is a responder.
package slackroles

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultMaxAge is how long the user groups found for a user are used
// before they are read from Slack again.
const DefaultMaxAge = 24 * time.Hour

// defaultAPIURL is the Slack Web API base URL when slack.apiURL is empty.
const defaultAPIURL = "https://slack.com/api/"

// UnmatchedRole is the role of a Slack user that neither slack.roleUsers
// nor slack.roleGroups names.
const UnmatchedRole = roles.Responder

// errNoSlack is returned by New for a config without a slack section.
var errNoSlack = errors.New("the slack section is required")

// Options set up a Lookup. Slack, Secrets and Pool are required.
type Options struct {
	// Slack holds roleUsers, roleGroups, the bot token and the API URL.
	Slack *config.Slack
	// Secrets resolves the bot token on every Slack call.
	Secrets registry.Secrets
	// Pool is the database that holds slack_identities.
	Pool *pgxpool.Pool
	// HTTP calls the Slack Web API. Nil means httpclient.New().
	HTTP *http.Client
	// MaxAge is how long stored user groups are used. Zero means
	// DefaultMaxAge.
	MaxAge time.Duration
	// Logger gets a warning when Slack cannot be reached and stored
	// groups are used instead. Nil logs nothing.
	Logger *slog.Logger
}

// Lookup finds Slack users' roles. It is safe for concurrent use.
type Lookup struct {
	slack   *config.Slack
	groups  []string
	secrets registry.Secrets
	pool    *pgxpool.Pool
	http    *http.Client
	apiURL  string
	maxAge  time.Duration
	log     *slog.Logger
	now     func() time.Time
}

var _ roles.Lookup = (*Lookup)(nil)

// New returns a Lookup for the slack section of a loaded config.
func New(opts Options) (*Lookup, error) {
	if opts.Slack == nil {
		return nil, errNoSlack
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}

	return &Lookup{
		slack:   opts.Slack,
		groups:  slices.Sorted(maps.Keys(opts.Slack.RoleGroups)),
		secrets: opts.Secrets,
		pool:    opts.Pool,
		http:    cmp.Or(opts.HTTP, httpclient.New()),
		apiURL:  cmp.Or(opts.Slack.APIURL, defaultAPIURL),
		maxAge:  maxAge,
		log:     log,
		now:     time.Now,
	}, nil
}

// Role returns the role of the Slack user userID in the workspace teamID,
// using the user groups stored for the user when they are recent enough.
func (l *Lookup) Role(ctx context.Context, teamID, userID string) (roles.Role, error) {
	return l.role(ctx, teamID, userID, false)
}

// Refresh returns the role of the Slack user like Role, but reads the
// user's groups from Slack first. Call it when a user is refused for their
// role, so a user who was just added to a group is not kept waiting a day.
func (l *Lookup) Refresh(ctx context.Context, teamID, userID string) (roles.Role, error) {
	return l.role(ctx, teamID, userID, true)
}

func (l *Lookup) role(ctx context.Context, teamID, userID string, fresh bool) (roles.Role, error) {
	if len(l.groups) == 0 {
		return RoleOf(l.slack, userID, nil), nil
	}

	q := db.New(l.pool)

	stored, err := q.GetSlackIdentity(ctx, userID)

	found := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("read the stored groups of %s: %w", userID, err)
	}

	if found && !fresh && l.now().Sub(stored.RefreshedAt) < l.maxAge {
		return RoleOf(l.slack, userID, stored.Groups), nil
	}

	groups, err := l.memberOf(ctx, teamID, userID)
	if err != nil {
		if !found {
			return 0, fmt.Errorf("read the user groups of %s: %w", userID, err)
		}

		l.log.WarnContext(ctx, "could not read a Slack user's groups; using the ones stored before", "user", userID, "error", err)

		return RoleOf(l.slack, userID, stored.Groups), nil
	}

	role := RoleOf(l.slack, userID, groups)
	name := string(config.RoleNameOf(role))

	err = q.SaveSlackIdentity(ctx, db.SaveSlackIdentityParams{
		SlackUserID: userID, TeamID: optional(teamID), Groups: groups, Role: &name, RefreshedAt: l.now(),
	})
	if err != nil {
		return 0, fmt.Errorf("store the groups of %s: %w", userID, err)
	}

	return role, nil
}

// memberOf returns the slack.roleGroups user groups that userID is in, in
// order, never nil.
func (l *Lookup) memberOf(ctx context.Context, teamID, userID string) ([]string, error) {
	token, err := secrets.ResolveText(ctx, l.secrets, l.slack.BotTokenSecret)
	if err != nil {
		return nil, fmt.Errorf("resolve the bot token: %w", err)
	}

	api := slack.New(token, slack.OptionAPIURL(l.apiURL), slack.OptionHTTPClient(l.http))

	var opts []slack.GetUserGroupMembersOption
	if teamID != "" {
		opts = append(opts, slack.GetUserGroupMembersOptionTeamID(teamID))
	}

	in := []string{}

	for _, group := range l.groups {
		members, err := api.GetUserGroupMembersContext(ctx, group, opts...)
		if err != nil {
			return nil, fmt.Errorf("usergroups.users.list %s: %w", group, err)
		}

		if slices.Contains(members, userID) {
			in = append(in, group)
		}
	}

	return in, nil
}

// RoleOf returns the role of userID, who is in groups: the highest of the
// role slack.roleUsers gives the user and those slack.roleGroups gives the
// groups, or UnmatchedRole when neither names the user.
func RoleOf(s *config.Slack, userID string, groups []string) roles.Role {
	role := s.RoleUsers[userID].Role()

	for _, g := range groups {
		role = max(role, s.RoleGroups[g].Role())
	}

	if role == 0 {
		return UnmatchedRole
	}

	return role
}

func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

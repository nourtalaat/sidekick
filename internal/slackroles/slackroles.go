// Package slackroles finds the Sidekick role of a Slack user from
// slack.roleUsers, slack.roleGroups and, with web UI login on, the web UI
// user whose email the Slack user has. Group membership is read from Slack
// with usergroups.users.list and the email with users.info, and both are
// kept in the slack_identities table for a day, so most lookups make no
// Slack call. The highest of these roles wins; a user none of them gives a
// role is a responder.
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

	"github.com/google/uuid"
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
	// MatchEmails matches Slack users to web UI users by email, read
	// with users.info (the users:read.email scope), so the web UI user's
	// role applies in Slack too. Set it when web UI login is on.
	MatchEmails bool
}

// Lookup finds Slack users' roles. It is safe for concurrent use.
type Lookup struct {
	slack       *config.Slack
	groups      []string
	matchEmails bool
	secrets     registry.Secrets
	pool        *pgxpool.Pool
	http        *http.Client
	apiURL      string
	maxAge      time.Duration
	log         *slog.Logger
	now         func() time.Time
}

// found is what one look at Slack and the users table found for a Slack
// user.
type found struct {
	groups   []string
	email    string
	userID   *uuid.UUID
	userRole *string
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
		slack:       opts.Slack,
		groups:      slices.Sorted(maps.Keys(opts.Slack.RoleGroups)),
		matchEmails: opts.MatchEmails,
		secrets:     opts.Secrets,
		pool:        opts.Pool,
		http:        cmp.Or(opts.HTTP, httpclient.New()),
		apiURL:      cmp.Or(opts.Slack.APIURL, defaultAPIURL),
		maxAge:      maxAge,
		log:         log,
		now:         time.Now,
	}, nil
}

// Role returns the role of the Slack user userID in the workspace teamID,
// using the user groups stored for the user when they are recent enough.
func (l *Lookup) Role(ctx context.Context, teamID, userID string) (roles.Role, error) {
	return l.role(ctx, teamID, userID, false)
}

// Refresh returns the role of the Slack user like Role, but reads the
// user's groups and email from Slack first. Call it when a user is refused
// for their role, so a user who was just added to a group, or who just
// signed in to the web UI, is not kept waiting a day.
func (l *Lookup) Refresh(ctx context.Context, teamID, userID string) (roles.Role, error) {
	return l.role(ctx, teamID, userID, true)
}

func (l *Lookup) role(ctx context.Context, teamID, userID string, fresh bool) (roles.Role, error) {
	if len(l.groups) == 0 && !l.matchEmails {
		return RoleOf(l.slack, userID, nil), nil
	}

	q := db.New(l.pool)

	stored, err := q.GetSlackIdentityWithUser(ctx, userID)

	have := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("read what is stored for %s: %w", userID, err)
	}

	if have && !fresh && l.now().Sub(stored.RefreshedAt) < l.maxAge {
		return l.roleOf(userID, stored.Groups, stored.UserRole), nil
	}

	f, err := l.lookUp(ctx, q, teamID, userID, stored, have)
	if err != nil {
		if !have {
			return 0, fmt.Errorf("look up %s in Slack: %w", userID, err)
		}

		l.log.WarnContext(ctx, "could not read a Slack user's groups; using the ones stored before", "user", userID, "error", err)

		return l.roleOf(userID, stored.Groups, stored.UserRole), nil
	}

	role := l.roleOf(userID, f.groups, f.userRole)
	name := string(config.RoleNameOf(role))

	err = q.SaveSlackIdentity(ctx, db.SaveSlackIdentityParams{
		SlackUserID: userID, TeamID: optional(teamID), Email: optional(f.email), UserID: f.userID,
		Groups: f.groups, Role: &name, RefreshedAt: l.now(),
	})
	if err != nil {
		return 0, fmt.Errorf("store what was found for %s: %w", userID, err)
	}

	return role, nil
}

// lookUp reads the user's groups and, when emails are matched, their email
// from Slack, and finds the web UI user with that email. When Slack does
// not give the email, for example because the app lacks the
// users:read.email scope, the email stored before is used.
func (l *Lookup) lookUp(ctx context.Context, q *db.Queries, teamID, userID string, stored db.GetSlackIdentityWithUserRow, have bool) (found, error) {
	token, err := secrets.ResolveText(ctx, l.secrets, l.slack.BotTokenSecret)
	if err != nil {
		return found{}, fmt.Errorf("resolve the bot token: %w", err)
	}

	api := slack.New(token, slack.OptionAPIURL(l.apiURL), slack.OptionHTTPClient(l.http))

	groups, err := l.memberOf(ctx, api, teamID, userID)
	if err != nil {
		return found{}, fmt.Errorf("read the user groups: %w", err)
	}

	f := found{groups: groups}
	if !l.matchEmails {
		return f, nil
	}

	f.email = l.emailOf(ctx, api, userID)
	if f.email == "" && have && stored.Email != nil {
		f.email = *stored.Email
	}

	if f.email == "" {
		return f, nil
	}

	user, err := q.FindUserByEmail(ctx, f.email)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}

	if err != nil {
		return found{}, fmt.Errorf("find the web UI user of %s: %w", userID, err)
	}

	f.userID, f.userRole = &user.ID, &user.Role

	return f, nil
}

// emailOf returns the email Slack has for userID, or "" when Slack does not
// give one.
func (l *Lookup) emailOf(ctx context.Context, api *slack.Client, userID string) string {
	user, err := api.GetUserInfoContext(ctx, userID)
	if err != nil {
		l.log.WarnContext(ctx, "could not read a Slack user's email; check that the app has the users:read.email scope", "user", userID, "error", err)

		return ""
	}

	return user.Profile.Email
}

// roleOf returns the highest of the roles the config maps give userID in
// groups and userRole, the role of the matched web UI user, or
// UnmatchedRole when none gives a role.
func (l *Lookup) roleOf(userID string, groups []string, userRole *string) roles.Role {
	role := mappedRole(l.slack, userID, groups)
	if userRole != nil {
		role = max(role, config.RoleName(*userRole).Role())
	}

	if role == 0 {
		return UnmatchedRole
	}

	return role
}

// memberOf returns the slack.roleGroups user groups that userID is in, in
// order, never nil.
func (l *Lookup) memberOf(ctx context.Context, api *slack.Client, teamID, userID string) ([]string, error) {
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
	if role := mappedRole(s, userID, groups); role != 0 {
		return role
	}

	return UnmatchedRole
}

// mappedRole returns the highest role slack.roleUsers and slack.roleGroups
// give userID in groups, or 0 when neither does.
func mappedRole(s *config.Slack, userID string, groups []string) roles.Role {
	role := s.RoleUsers[userID].Role()

	for _, g := range groups {
		role = max(role, s.RoleGroups[g].Role())
	}

	return role
}

func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// SlackMentionIntake is the intake name of events that start because
// someone mentioned Sidekick in Slack. It is reserved: no webhook endpoint
// or listener may use it, and routes can select mentions with it in
// match.intake.
const SlackMentionIntake = "slack-mention"

// SlackMode says how the Slack app receives events.
type SlackMode string

// Slack modes.
const (
	// SlackModeSocket receives events over a Socket Mode connection, so
	// Sidekick needs no public URL. It needs slack.appTokenSecret.
	SlackModeSocket SlackMode = "socket"
	// SlackModeEvents receives events at POST /slack/events, checked with
	// slack.signingSecret. Slack must be able to reach that URL.
	SlackModeEvents SlackMode = "events"
)

// DefaultSlackMode is the mode of a slack section that sets none.
const DefaultSlackMode = SlackModeSocket

// The named groups of a listener's extract patterns that set event fields.
// Any other named group sets a label of that name.
const (
	ExtractAlert    = "alert"
	ExtractService  = "service"
	ExtractSeverity = "severity"
)

// RoleName is the name of a Sidekick role in the config file.
type RoleName string

// Role names, lowest first.
const (
	RoleViewer    RoleName = "viewer"
	RoleResponder RoleName = "responder"
	RoleApprover  RoleName = "approver"
	RoleAdmin     RoleName = "admin"
)

// knownRoleNames are the role names config accepts, lowest first.
var knownRoleNames = []RoleName{RoleViewer, RoleResponder, RoleApprover, RoleAdmin}

// Role returns the role n names, or 0 for a name that is not a role.
func (n RoleName) Role() roles.Role {
	return roles.Role(slices.Index(knownRoleNames, n) + 1)
}

// RoleNameOf returns the name of r, or "" for a value that is not a role.
func RoleNameOf(r roles.Role) RoleName {
	if r < roles.Viewer || int(r) > len(knownRoleNames) {
		return ""
	}

	return knownRoleNames[r-1]
}

// The sender kinds a listener can match.
var knownListenerSenders = []event.SenderKind{event.SenderHuman, event.SenderBot}

var (
	// channelPattern is a channel name with its "#", or a channel ID.
	channelPattern = regexp.MustCompile(`^(#[a-z0-9][a-z0-9._-]{0,79}|[CG][A-Z0-9]{2,})$`)
	// botIDPattern is a Slack bot ID, such as B01ABCDEF.
	botIDPattern = regexp.MustCompile(`^B[A-Z0-9]{2,}$`)
	// userIDPattern is a Slack user ID, such as U01ABCDEF or W01ABCDEF.
	userIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	// userGroupIDPattern is a Slack user group ID, such as S01ABCDEF.
	userGroupIDPattern = regexp.MustCompile(`^S[A-Z0-9]{2,}$`)
)

// Slack holds the Slack app's credentials and how it receives events
// (IN-4).
type Slack struct {
	// BotTokenSecret holds the bot token (xoxb-...), which posts messages
	// and reads the messages Sidekick is asked about.
	BotTokenSecret registry.SecretRef `yaml:"botTokenSecret"`
	// AppTokenSecret holds the app-level token (xapp-...) with
	// connections:write that opens Socket Mode connections. Without it,
	// in socket mode, Sidekick only posts and receives nothing.
	AppTokenSecret registry.SecretRef `yaml:"appTokenSecret"`
	// SigningSecret holds the signing secret that checks Events API
	// requests. Mode events needs it.
	SigningSecret registry.SecretRef `yaml:"signingSecret"`
	// Mode is socket or events.
	Mode SlackMode `yaml:"mode"`
	// TrustedBots are the bot IDs whose messages count as trusted, in
	// listeners marked trusted.
	TrustedBots []string `yaml:"trustedBots"`
	// APIURL is the base URL of the Slack Web API, ending in "/". Empty
	// means https://slack.com/api/.
	APIURL string `yaml:"apiURL"`
	// RoleGroups maps Slack user group IDs to roles. A user in several
	// groups gets the highest of their roles.
	RoleGroups map[string]RoleName `yaml:"roleGroups"`
	// RoleUsers maps Slack user IDs to roles, for teams without user
	// groups. The higher of this role and the one from RoleGroups wins.
	RoleUsers map[string]RoleName `yaml:"roleUsers"`
}

// Listener picks the Slack or Teams messages in one channel that start an
// investigation (IN-5, IN-8). Every field of Match that is set must match.
type Listener struct {
	// Source is the chat app of the channel: slack, the default, or teams.
	Source ListenerSource `yaml:"source"`
	// Channel is a Slack channel name such as "#alerts" or channel ID, or
	// a Teams channel ID such as 19:abc123@thread.tacv2.
	Channel string `yaml:"channel"`
	// Match selects messages in the channel.
	Match ListenerMatch `yaml:"match"`
	// Extract are regular expressions over the message text. Their named
	// groups set the alert name (alert), the service (service), the
	// severity (severity) and, for any other name, a label of that name.
	// The first pattern that sets a field wins.
	Extract []string `yaml:"extract"`
	// Recovery is a regular expression. A message it matches becomes a
	// resolved event instead of an alert.
	Recovery string `yaml:"recovery"`
	// Trusted marks events from the bots in slack.trustedBots, or
	// teams.trustedBots for a Teams listener, as trusted (ACT-6). Every
	// other sender stays untrusted.
	Trusted bool `yaml:"trusted"`
}

// ListenerMatch selects messages by sender and text.
type ListenerMatch struct {
	Sender ListenerSender `yaml:"sender"`
	// Text is a regular expression over the message text, with the text of
	// its attachments and blocks added on new lines.
	Text string `yaml:"text"`
}

// ListenerSender selects messages by who sent them. When any of Names,
// Bots and Users is set, the sender must be in one of them.
type ListenerSender struct {
	// Kind is human or bot; empty matches both.
	Kind event.SenderKind `yaml:"kind"`
	// Names are bot names, such as SigNoz, compared without case.
	Names []string `yaml:"names"`
	// Bots are Slack bot IDs, such as B01ABCDEF, or for Teams the bot's
	// "28:" ID.
	Bots []string `yaml:"bots"`
	// Users are Slack user IDs, such as U01ABCDEF, or for Teams the users'
	// Microsoft Entra object IDs.
	Users []string `yaml:"users"`
}

// Connects reports whether the Slack app receives events: in socket mode
// when an app token is set, and always in events mode.
func (s *Slack) Connects() bool {
	if s == nil {
		return false
	}

	return s.Mode == SlackModeEvents || s.AppTokenSecret != (registry.SecretRef{})
}

func (s *Slack) setDefaults() {
	s.Mode = DefaultSlackMode
}

// checkSlack checks the slack section and the listeners, which need it.
func (v *validator) checkSlack() {
	s := v.cfg.Slack
	if s != nil {
		v.checkSlackSection(s)
	}

	for _, name := range sortedKeys(v.cfg.Listeners) {
		v.checkListener(joinPath("listeners", name), name, v.cfg.Listeners[name])
	}

	if v.hasListeners(ListenerSourceSlack) && !s.Connects() {
		v.errs.add("listeners", "listeners need the Slack app to receive events: set slack.appTokenSecret, or slack.mode: events with slack.signingSecret")
	}

	if v.hasListeners(ListenerSourceTeams) && v.cfg.Teams == nil {
		v.errs.add("listeners", "listeners with source teams need the teams section, the Teams bot that receives their messages")
	}
}

func (v *validator) checkSlackSection(s *Slack) {
	if s.BotTokenSecret == (registry.SecretRef{}) {
		v.errs.add("slack.botTokenSecret", "required")
	}

	switch s.Mode {
	case SlackModeSocket:
	case SlackModeEvents:
		if s.SigningSecret == (registry.SecretRef{}) {
			v.errs.add("slack.signingSecret", "required with mode events")
		}
	default:
		v.errs.add("slack.mode", "expected socket or events, got %q%s", s.Mode, didYouMean(string(s.Mode), []string{string(SlackModeSocket), string(SlackModeEvents)}))
	}

	for i, id := range s.TrustedBots {
		if !botIDPattern.MatchString(id) {
			v.errs.add(fmt.Sprintf("slack.trustedBots[%d]", i), "expected a bot ID such as B01ABCDEF, got %q", id)
		}
	}

	if s.APIURL != "" && (!isHTTPURL(s.APIURL) || s.APIURL[len(s.APIURL)-1] != '/') {
		v.errs.add("slack.apiURL", `expected an absolute http or https URL ending in "/", such as https://slack.com/api/, got %q`, s.APIURL)
	}

	v.checkRoleMap("slack.roleGroups", s.RoleGroups, userGroupIDPattern, "a user group ID such as S01ABCDEF")
	v.checkRoleMap("slack.roleUsers", s.RoleUsers, userIDPattern, "a user ID such as U01ABCDEF")
}

// checkRoleMap checks that every key of m matches pattern and every value
// is a role name.
func (v *validator) checkRoleMap(path string, m map[string]RoleName, pattern *regexp.Regexp, want string) {
	names := asStrings(knownRoleNames)

	for _, id := range sortedKeys(m) {
		at := joinPath(path, id)
		if !pattern.MatchString(id) {
			v.errs.add(at, "expected %s, got %q", want, id)
		}

		if role := m[id]; role.Role() == 0 {
			v.errs.add(at, "unknown role %q, expected one of %s%s", role, strings.Join(names, ", "), didYouMean(string(role), names))
		}
	}
}

func (v *validator) checkListener(at, name string, l Listener) {
	v.checkIntakeName(at, name)

	if _, clash := v.cfg.Intake.Webhooks[name]; clash {
		v.errs.add(at, "listener %q has the name of a webhook endpoint; events of both would share one intake name", name)
	}

	switch {
	case !v.checkListenerSource(at, l):
	case l.OnTeams():
		v.checkTeamsListener(at, l)
	default:
		v.checkSlackListener(at, l)
	}

	sender := l.Match.Sender
	if sender.Kind != "" && !slices.Contains(knownListenerSenders, sender.Kind) {
		v.errs.add(at+".match.sender.kind", "expected human or bot, got %q%s", sender.Kind, didYouMean(string(sender.Kind), asStrings(knownListenerSenders)))
	}

	v.checkRegexp(at+".match.text", l.Match.Text)
	v.checkRegexp(at+".recovery", l.Recovery)

	for i, pattern := range l.Extract {
		p := fmt.Sprintf("%s.extract[%d]", at, i)

		re, err := regexp.Compile(pattern)
		if err != nil {
			v.errs.add(p, "invalid regular expression: %v", err)

			continue
		}

		if !slices.ContainsFunc(re.SubexpNames(), func(n string) bool { return n != "" }) {
			v.errs.add(p, "has no named group such as (?P<service>...); only named groups set fields")
		}
	}
}

// checkSlackListener checks the fields of a listener on a Slack channel
// whose rules differ from Teams'.
func (v *validator) checkSlackListener(at string, l Listener) {
	switch {
	case l.Channel == "":
		v.errs.add(at+".channel", "required")
	case !channelPattern.MatchString(l.Channel):
		v.errs.add(at+".channel", `expected a channel name such as "#alerts" or a channel ID such as C01ABCDEF, got %q`, l.Channel)
	}

	v.checkIDs(at+".match.sender.bots", l.Match.Sender.Bots, botIDPattern, "a bot ID such as B01ABCDEF")
	v.checkIDs(at+".match.sender.users", l.Match.Sender.Users, userIDPattern, "a user ID such as U01ABCDEF")

	if l.Trusted && (v.cfg.Slack == nil || len(v.cfg.Slack.TrustedBots) == 0) {
		v.errs.add(at+".trusted", "has no effect without slack.trustedBots, the bots whose messages are trusted")
	}
}

// checkIntakeName checks the name of a webhook endpoint or listener, which
// becomes the intake name of its events.
func (v *validator) checkIntakeName(path, name string) {
	v.checkName(path, name)

	switch name {
	case SlackMentionIntake:
		v.errs.add(path, "name %q is reserved for events from mentions of Sidekick in Slack", name)
	case TeamsMentionIntake:
		v.errs.add(path, "name %q is reserved for events from mentions of Sidekick in Teams", name)
	}
}

// intakeNames returns the names a route's match.intake can name: the
// webhook endpoints, the listeners and, with a slack or teams section,
// the mentions in that app.
func (v *validator) intakeNames() []string {
	names := slices.Collect(maps.Keys(v.cfg.Intake.Webhooks))
	names = append(names, slices.Collect(maps.Keys(v.cfg.Listeners))...)
	names = append(names, v.extraIntakes...)

	if v.cfg.Slack != nil {
		names = append(names, SlackMentionIntake)
	}

	if v.cfg.Teams != nil {
		names = append(names, TeamsMentionIntake)
	}

	return names
}

func (v *validator) checkIDs(path string, ids []string, pattern *regexp.Regexp, want string) {
	for i, id := range ids {
		if !pattern.MatchString(id) {
			v.errs.add(fmt.Sprintf("%s[%d]", path, i), "expected %s, got %q", want, id)
		}
	}
}

func (v *validator) checkRegexp(path, pattern string) {
	if pattern == "" {
		return
	}

	if _, err := regexp.Compile(pattern); err != nil {
		v.errs.add(path, "invalid regular expression: %v", err)
	}
}

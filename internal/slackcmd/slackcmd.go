//go:generate mockgen -source slackcmd.go -destination mocks_slackcmd.go -package slackcmd -write_package_comment=false

// Package slackcmd answers the /sidekick slash command: adding and
// removing runtime listeners, listing listeners and routes, setting a few
// fields of runtime routes, investigating a linked message, and status and
// help. Each change is checked against the user's role first: listeners
// need the responder role and routes need admin. Over Socket Mode the
// Slack app hands it commands; with the Events API it serves
// POST /slack/commands.
package slackcmd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/slackapp"
)

// The subcommands.
const (
	cmdListen      = "listen"
	cmdUnlisten    = "unlisten"
	cmdListeners   = "listeners"
	cmdRoutes      = "routes"
	cmdRoute       = "route"
	cmdInvestigate = "investigate"
	cmdStatus      = "status"
	cmdHelp        = "help"
)

// responseEphemeral shows a reply only to the user who sent the command.
const responseEphemeral = "ephemeral"

// eventKeyPrefix starts the event key of a message investigated with
// /sidekick investigate. The rest is the command's trigger ID, so a
// command Slack sends again is stored once.
const eventKeyPrefix = "command:"

// msgInternal is the reply when Sidekick itself failed.
const msgInternal = "Sidekick could not do that because of a problem on its side. Its logs say more."

// helpText lists the subcommands.
const helpText = "*/sidekick* commands:\n" +
	"• `/sidekick listen #channel [bot:<name or ID>] [pattern:<regex>] [name:<name>]`: start investigating messages in a channel. Needs the responder role.\n" +
	"• `/sidekick unlisten <name>`: stop a listener added with listen. Needs the responder role.\n" +
	"• `/sidekick listeners`: list the listeners.\n" +
	"• `/sidekick routes`: list the routes, in the order they are tried.\n" +
	"• `/sidekick route set <name> <field> <value>`: change a field of a route added from Slack, or add the route. Needs the admin role. `/sidekick route set` lists the fields.\n" +
	"• `/sidekick investigate <message link>`: investigate any message. Needs the responder role.\n" +
	"• `/sidekick status`: what Sidekick runs with, and your role.\n" +
	"• `/sidekick help`: this list."

// Investigator starts an investigation of a linked message.
// *slackapp.Handler is one.
type Investigator interface {
	InvestigateLink(ctx context.Context, teamID, appID, key, link string) error
}

// RoleLookup finds Slack users' roles. Refresh is like Role but reads the
// user's groups again, for a user who was refused.
// *slackroles.Lookup is one.
type RoleLookup interface {
	roles.Lookup
	Refresh(ctx context.Context, teamID, userID string) (roles.Role, error)
}

// Options set up a Handler. Every field but Version is required.
type Options struct {
	// File is the loaded config.
	File *config.Config
	// Store saves runtime listeners and routes.
	Store *rules.Store
	// Rules are the rules in effect on this replica.
	Rules *rules.Cache
	// Roles finds users' roles.
	Roles RoleLookup
	// Investigator starts investigations of linked messages.
	Investigator Investigator
	// Logger gets a line per change and per failure. Command text is not
	// logged.
	Logger *slog.Logger
	// Version is the Sidekick version status shows.
	Version string
	// Timeout bounds answering one command, so Slack gets the reply within
	// its 3 second limit. Zero means slackapp.AckBudget.
	Timeout time.Duration
}

// Handler answers /sidekick commands. It is safe for concurrent use.
type Handler struct {
	file         *config.Config
	store        *rules.Store
	rules        *rules.Cache
	roles        RoleLookup
	investigator Investigator
	log          *slog.Logger
	version      string
	timeout      time.Duration
}

var _ slackapp.CommandHandler = (*Handler)(nil)

// response is the reply to a command.
type response struct {
	ResponseType string `json:"response_type"`
	Text         string `json:"text"`
}

// userError is a problem with what the user asked, shown to them as it
// is.
type userError struct {
	msg string
}

func (e *userError) Error() string {
	return e.msg
}

// New returns a Handler.
func New(opts Options) *Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Handler{
		file:         opts.File,
		store:        opts.Store,
		rules:        opts.Rules,
		roles:        opts.Roles,
		investigator: opts.Investigator,
		log:          log,
		version:      opts.Version,
		timeout:      cmp.Or(opts.Timeout, slackapp.AckBudget),
	}
}

// HandleCommand answers one /sidekick command with a reply only its sender
// sees. Problems with the command, a role too low and failures are all
// answered in the reply, so the error is only for a reply that cannot be
// encoded.
func (h *Handler) HandleCommand(ctx context.Context, cmd slack.SlashCommand) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	text, err := h.run(ctx, cmd)
	if err != nil {
		text = h.errorText(ctx, cmd, err)
	}

	body, err := json.Marshal(response{ResponseType: responseEphemeral, Text: text})
	if err != nil {
		return nil, fmt.Errorf("encode the reply: %w", err)
	}

	return body, nil
}

// run runs the subcommand in cmd and returns the reply.
func (h *Handler) run(ctx context.Context, cmd slack.SlashCommand) (string, error) {
	args := splitArgs(cmd.Text)
	if len(args) == 0 {
		return helpText, nil
	}

	sub, rest := strings.ToLower(args[0]), args[1:]

	switch sub {
	case cmdListen:
		return h.listen(ctx, cmd, rest)
	case cmdUnlisten:
		return h.unlisten(ctx, cmd, rest)
	case cmdListeners:
		return h.listListeners(ctx, cmd)
	case cmdRoutes:
		return h.listRoutes(ctx, cmd)
	case cmdRoute:
		return h.route(ctx, cmd, rest)
	case cmdInvestigate:
		return h.investigate(ctx, cmd, rest)
	case cmdStatus:
		return h.status(ctx, cmd)
	case cmdHelp:
		return helpText, nil
	default:
		return "", &userError{msg: "There is no `/sidekick " + escape(sub) + "` command.\n\n" + helpText}
	}
}

// require returns nil when the sender of cmd has the role need. A user
// refused for their role has their groups read again first, in case they
// were just added to one.
func (h *Handler) require(ctx context.Context, cmd slack.SlashCommand, need roles.Role) error {
	role, err := h.roles.Role(ctx, cmd.TeamID, cmd.UserID)
	if err != nil {
		return fmt.Errorf("look up the role of %s: %w", cmd.UserID, err)
	}

	if role.Has(need) {
		return nil
	}

	if role, err = h.roles.Refresh(ctx, cmd.TeamID, cmd.UserID); err != nil {
		return fmt.Errorf("look up the role of %s again: %w", cmd.UserID, err)
	}

	if role.Has(need) {
		return nil
	}

	return &userError{msg: fmt.Sprintf(
		"This needs the %s role, and you are %s. Roles come from `slack.roleGroups` and `slack.roleUsers` in the Sidekick config.",
		config.RoleNameOf(need), withArticle(string(config.RoleNameOf(role))),
	)}
}

// errorText turns an error from a subcommand into its reply. Problems
// with the request are shown as they are; anything else is logged and
// answered with msgInternal.
func (h *Handler) errorText(ctx context.Context, cmd slack.SlashCommand, err error) string {
	var (
		userErr  *userError
		problems config.Errors
	)

	switch {
	case errors.As(err, &userErr):
		return userErr.msg
	case errors.As(err, &problems):
		lines := make([]string, len(problems))
		for i, p := range problems {
			lines[i] = "• " + escape(p.String())
		}

		return "That does not pass the config checks:\n" + strings.Join(lines, "\n")
	case errors.Is(err, rules.ErrConflict):
		return "Someone changed the same rule at the same moment. Try again."
	default:
		h.log.ErrorContext(ctx, "a /sidekick command failed", "user", cmd.UserID, "error", err)

		return msgInternal
	}
}

// withArticle puts "a" or "an" before a role name.
func withArticle(name string) string {
	if name != "" && strings.ContainsRune("aeiou", rune(name[0])) {
		return "an " + name
	}

	return "a " + name
}

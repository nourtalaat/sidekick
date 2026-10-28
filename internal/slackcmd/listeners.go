package slackcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/slack-go/slack"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/roles"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// The options of /sidekick listen.
const (
	optBot     = "bot"
	optPattern = "pattern"
	optName    = "name"
)

const listenUsage = "Usage: `/sidekick listen #channel [bot:<name or ID>] [pattern:<regex>] [name:<name>]`"

// listen adds a runtime listener.
func (h *Handler) listen(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if err := h.require(ctx, cmd, roles.Responder); err != nil {
		return "", err
	}

	name, l, err := parseListen(args)
	if err != nil {
		return "", err
	}

	if err := h.store.AddListener(ctx, cmd.UserID, name, l); err != nil {
		switch {
		case errors.Is(err, rules.ErrFileRule), errors.Is(err, rules.ErrExists):
			return "", &userError{msg: fmt.Sprintf("A listener named %s exists already. Choose another name with `name:<name>`.", code(name))}
		default:
			return "", fmt.Errorf("add listener %s: %w", name, err)
		}
	}

	h.log.InfoContext(ctx, "runtime listener added", "listener", name, "user", cmd.UserID)

	return fmt.Sprintf("Listening in %s as listener %s%s. Routes pick its events with `match.intake` %s. Invite Sidekick to the channel if it is not there yet.",
		channelText(l.Channel), code(name), describeListener(l), code(name)), nil
}

// unlisten removes a runtime listener.
func (h *Handler) unlisten(ctx context.Context, cmd slack.SlashCommand, args []string) (string, error) {
	if err := h.require(ctx, cmd, roles.Responder); err != nil {
		return "", err
	}

	if len(args) != 1 {
		return "", &userError{msg: "Usage: `/sidekick unlisten <name>`. `/sidekick listeners` lists the names."}
	}

	name := args[0]

	err := h.store.RemoveListener(ctx, cmd.UserID, name)

	switch {
	case errors.Is(err, rules.ErrFileRule):
		return "", &userError{msg: fmt.Sprintf("Listener %s is set in the Sidekick config file; only a config change can remove it.", code(name))}
	case errors.Is(err, rules.ErrNotFound):
		return "", &userError{msg: fmt.Sprintf("No listener added from Slack is named %s. `/sidekick listeners` lists them.", code(name))}
	case err != nil:
		return "", fmt.Errorf("remove listener %s: %w", name, err)
	}

	h.log.InfoContext(ctx, "runtime listener removed", "listener", name, "user", cmd.UserID)

	return fmt.Sprintf("Listener %s removed.", code(name)), nil
}

// listListeners lists the listeners in effect, and the runtime ones that
// are not.
func (h *Handler) listListeners(ctx context.Context, cmd slack.SlashCommand) (string, error) {
	if err := h.require(ctx, cmd, roles.Viewer); err != nil {
		return "", err
	}

	e := h.rules.Current()
	if len(e.Listeners) == 0 {
		return "No listeners yet. `/sidekick listen #channel` adds one." + skippedText(e, rules.KindListener), nil
	}

	lines := make([]string, 0, len(e.Listeners))
	for _, l := range e.Listeners {
		lines = append(lines, fmt.Sprintf("• %s (%s) in %s%s", code(l.Name), layerText(l.Layer), channelText(l.Config.Channel), describeListener(l.Config)))
	}

	return "*Listeners*\n" + strings.Join(lines, "\n") + skippedText(e, rules.KindListener), nil
}

// parseListen reads the arguments of /sidekick listen.
func parseListen(args []string) (string, config.Listener, error) {
	if len(args) == 0 {
		return "", config.Listener{}, &userError{msg: listenUsage}
	}

	channel, channelName := channelArg(args[0])
	l := config.Listener{Channel: channel}
	name := listenerName(channelName)

	for _, arg := range args[1:] {
		key, value, ok := option(arg)
		if !ok || value == "" {
			return "", config.Listener{}, &userError{msg: fmt.Sprintf("%s is not an option. %s", code(arg), listenUsage)}
		}

		switch key {
		case optBot:
			l.Match.Sender.Kind = event.SenderBot
			if botIDPattern.MatchString(value) {
				l.Match.Sender.Bots = append(l.Match.Sender.Bots, value)
			} else {
				l.Match.Sender.Names = append(l.Match.Sender.Names, value)
			}
		case optPattern:
			l.Match.Text = value
		case optName:
			name = value
		default:
			return "", config.Listener{}, &userError{msg: fmt.Sprintf("%s is not an option. %s", code(key+":"), listenUsage)}
		}
	}

	if name == "" {
		return "", config.Listener{}, &userError{msg: "Sidekick cannot make a listener name from that channel. Give one with `name:<name>`."}
	}

	return name, l, nil
}

// describeListener says what a listener matches, starting with ": ", or
// "" when it takes every message.
func describeListener(l config.Listener) string {
	var parts []string

	s := l.Match.Sender
	if s.Kind != "" {
		parts = append(parts, "messages from a "+string(s.Kind))
	}

	if senders := append(append(append([]string(nil), s.Names...), s.Bots...), s.Users...); len(senders) > 0 {
		quoted := make([]string, len(senders))
		for i, name := range senders {
			quoted[i] = code(name)
		}

		parts = append(parts, "sent by "+strings.Join(quoted, ", "))
	}

	if l.Match.Text != "" {
		parts = append(parts, "matching "+code(l.Match.Text))
	}

	if len(parts) == 0 {
		return ": every message"
	}

	return ": " + strings.Join(parts, ", ")
}

// channelText shows a listener's channel: a channel ID as a link Slack
// turns into its name, a name as it is.
func channelText(channel string) string {
	if strings.HasPrefix(channel, "#") || !channelIDPattern.MatchString(channel) {
		return escape(channel)
	}

	return "<#" + channel + ">"
}

func layerText(l rules.Layer) string {
	if l == rules.LayerFile {
		return "config file"
	}

	return "added from Slack"
}

// skippedText lists the runtime rules of kind that are stored but not in
// effect, starting on a new line, or "".
func skippedText(e *rules.Effective, kind string) string {
	var lines []string

	for _, group := range []struct {
		skipped []rules.Skipped
		what    string
	}{
		{e.Shadowed, "not used: the config file has a rule of the same name"},
		{e.Invalid, "not used: it fails the config checks"},
		{e.Disabled, "turned off"},
	} {
		for _, s := range group.skipped {
			if s.Kind == kind {
				lines = append(lines, fmt.Sprintf("• %s: %s", code(s.Name), group.what))
			}
		}
	}

	if len(lines) == 0 {
		return ""
	}

	return "\n*Added from Slack but not in effect*\n" + strings.Join(lines, "\n")
}

package slackapp

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/listen"
)

// listener is a configured Slack listener with its patterns compiled.
type listener struct {
	listen.Rules
}

// matchesChannel reports whether the listener watches the channel with
// id, whose name, without "#", is name. name is only needed when the
// listener names its channel by name.
func (l listener) matchesChannel(id, name string) bool {
	if channel, byName := strings.CutPrefix(l.Channel, "#"); byName {
		return name != "" && channel == name
	}

	return l.Channel == id
}

// byName reports whether the listener names its channel rather than giving
// its ID.
func (l listener) byName() bool {
	return strings.HasPrefix(l.Channel, "#")
}

// matches reports whether m, whose flattened text is text, passes the
// listener's sender and text checks. The channel is checked apart.
func (l listener) matches(m message, text string) bool {
	return l.matchesSender(m) && l.MatchesText(text)
}

func (l listener) matchesSender(m message) bool {
	s := l.Sender

	switch {
	case s.Kind == event.SenderBot && !m.isBot():
		return false
	case s.Kind == event.SenderHuman && m.isBot():
		return false
	case len(s.Names) == 0 && len(s.Bots) == 0 && len(s.Users) == 0:
		return true
	}

	if m.isBot() {
		name := m.senderName()

		return slices.Contains(s.Bots, m.botID()) ||
			(name != "" && slices.ContainsFunc(s.Names, func(n string) bool { return strings.EqualFold(n, name) }))
	}

	return slices.Contains(s.Users, m.User)
}

// compileListeners compiles the configured Slack listeners, in name order,
// which is the order messages are matched in. Teams listeners are left out.
func compileListeners(cfg map[string]config.Listener) ([]listener, error) {
	rules, err := listen.CompileAll(cfg, config.ListenerSourceSlack)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	out := make([]listener, 0, len(rules))
	for _, r := range rules {
		out = append(out, listener{Rules: r})
	}

	return out, nil
}

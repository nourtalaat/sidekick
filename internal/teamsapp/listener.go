package teamsapp

import (
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/listen"
)

// listener is a configured Teams listener with its patterns compiled.
type listener struct {
	listen.Rules
}

// matches reports whether a, whose flattened text is text, was sent in
// the listener's channel and passes its sender and text checks.
func (l listener) matches(a activity, text string) bool {
	return a.channel() == l.Channel && l.matchesSender(a) && l.MatchesText(text)
}

// matchesSender checks the sender kind and, when any is set, the bot IDs,
// bot names and users. Bots are matched by their "28:" ID or name, people
// by their Microsoft Entra object ID.
func (l listener) matchesSender(a activity) bool {
	s := l.Sender

	switch {
	case s.Kind == event.SenderBot && !a.isBot():
		return false
	case s.Kind == event.SenderHuman && a.isBot():
		return false
	case len(s.Names) == 0 && len(s.Bots) == 0 && len(s.Users) == 0:
		return true
	}

	if a.isBot() {
		name := a.From.Name

		return slices.ContainsFunc(s.Bots, func(id string) bool { return strings.EqualFold(id, a.From.ID) }) ||
			(name != "" && slices.ContainsFunc(s.Names, func(n string) bool { return strings.EqualFold(n, name) }))
	}

	return a.From.AADObjectID != "" && slices.ContainsFunc(s.Users, func(id string) bool { return strings.EqualFold(id, a.From.AADObjectID) })
}

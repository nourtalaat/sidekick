package slackapp

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
)

// listener is a configured listener with its patterns compiled.
type listener struct {
	name    string
	channel string
	sender  config.ListenerSender
	// text, recovery and extract are nil or empty when not configured.
	text     *regexp.Regexp
	recovery *regexp.Regexp
	extract  []*regexp.Regexp
	trusted  bool
}

// fields are what a listener's extract patterns found in a message.
type fields struct {
	alert    string
	service  string
	severity event.Severity
	labels   map[string]string
}

// matchesChannel reports whether the listener watches the channel with
// id, whose name, without "#", is name. name is only needed when the
// listener names its channel by name.
func (l listener) matchesChannel(id, name string) bool {
	if channel, byName := strings.CutPrefix(l.channel, "#"); byName {
		return name != "" && channel == name
	}

	return l.channel == id
}

// byName reports whether the listener names its channel rather than giving
// its ID.
func (l listener) byName() bool {
	return strings.HasPrefix(l.channel, "#")
}

// matches reports whether m, whose flattened text is text, passes the
// listener's sender and text checks. The channel is checked apart.
func (l listener) matches(m message, text string) bool {
	return l.matchesSender(m) && (l.text == nil || l.text.MatchString(text))
}

func (l listener) matchesSender(m message) bool {
	s := l.sender

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

// isRecovery reports whether text matches the listener's recovery pattern.
func (l listener) isRecovery(text string) bool {
	return l.recovery != nil && l.recovery.MatchString(text)
}

// fields runs the extract patterns over text, in order. The first pattern
// that sets a field wins; empty matches set nothing.
func (l listener) fields(text string) fields {
	f := fields{severity: event.SeverityUnknown}

	for _, re := range l.extract {
		match := re.FindStringSubmatch(text)
		if match == nil {
			continue
		}

		for i, name := range re.SubexpNames() {
			value := strings.TrimSpace(match[i])
			if name == "" || value == "" {
				continue
			}

			f.set(name, value)
		}
	}

	return f
}

// set sets the field that the named group name fills, unless it is set.
func (f *fields) set(name, value string) {
	switch name {
	case config.ExtractAlert:
		if f.alert == "" {
			f.alert = value
		}
	case config.ExtractService:
		if f.service == "" {
			f.service = value
		}
	case config.ExtractSeverity:
		if f.severity == event.SeverityUnknown {
			f.severity = intake.ParseSeverity(value)
		}
	default:
		if f.labels == nil {
			f.labels = map[string]string{}
		}

		if _, ok := f.labels[name]; !ok {
			f.labels[name] = value
		}
	}
}

// compileListeners compiles the configured listeners, in name order, which
// is the order messages are matched in.
func compileListeners(cfg map[string]config.Listener) ([]listener, error) {
	out := make([]listener, 0, len(cfg))

	for _, name := range slices.Sorted(maps.Keys(cfg)) {
		c := cfg[name]
		l := listener{name: name, channel: c.Channel, sender: c.Match.Sender, trusted: c.Trusted}

		var err error
		if l.text, err = optionalRegexp(c.Match.Text); err != nil {
			return nil, fmt.Errorf("listener %s: match.text: %w", name, err)
		}

		if l.recovery, err = optionalRegexp(c.Recovery); err != nil {
			return nil, fmt.Errorf("listener %s: recovery: %w", name, err)
		}

		for i, pattern := range c.Extract {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("listener %s: extract[%d]: %w", name, i, err)
			}

			l.extract = append(l.extract, re)
		}

		out = append(out, l)
	}

	return out, nil
}

// optionalRegexp compiles pattern, or returns nil for "".
func optionalRegexp(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}

	return re, nil
}

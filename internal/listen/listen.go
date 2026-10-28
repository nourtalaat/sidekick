// Package listen holds the listener rules that the Slack app and the Teams
// bot share: a listener's text pattern, its recovery pattern, and the
// extract patterns that set an event's alert name, service, severity and
// labels from message text. Who sent a message, and which channel it is in,
// is checked by each app, since their messages differ.
package listen

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/source"
)

// MaxTitleBytes is the longest event title taken from a message's first
// line.
const MaxTitleBytes = 200

// Rules are one configured listener with its patterns compiled.
type Rules struct {
	// Name is the listener's name, the intake of its events.
	Name string
	// Channel is the channel the listener watches, as configured.
	Channel string
	// Sender selects messages by who sent them.
	Sender config.ListenerSender
	// Trusted marks events from trusted bots as trusted.
	Trusted bool

	// text, recovery and extract are nil or empty when not configured.
	text     *regexp.Regexp
	recovery *regexp.Regexp
	extract  []*regexp.Regexp
}

// Fields are what a listener's extract patterns found in a message.
type Fields struct {
	Alert    string
	Service  string
	Severity event.Severity
	Labels   map[string]string
}

// MatchesText reports whether text passes the listener's text pattern; a
// listener without one takes every text.
func (r Rules) MatchesText(text string) bool {
	return r.text == nil || r.text.MatchString(text)
}

// Apply fills ev from text, the message text the listener matched: the
// fields the extract patterns find, a fingerprint of the listener's name
// with the alert name and labels when any were found, so an alert and its
// recovery share it, and the resolved kind when the recovery pattern
// matches. A fingerprint ev already has is kept when nothing was found.
func (r Rules) Apply(ev *event.Event, text string) {
	f := r.Fields(text)
	ev.AlertName, ev.Service, ev.Severity, ev.Labels = f.Alert, f.Service, f.Severity, f.Labels

	if f.Alert != "" || len(f.Labels) > 0 {
		ev.Fingerprint = event.Fingerprint(r.Name, f.Alert, f.Labels, slices.Collect(maps.Keys(f.Labels)))
	}

	if r.IsRecovery(text) {
		ev.Kind = event.KindResolved
	}
}

// Fields runs the extract patterns over text, in order. The first pattern
// that sets a field wins; empty matches set nothing.
func (r Rules) Fields(text string) Fields {
	f := Fields{Severity: event.SeverityUnknown}

	for _, re := range r.extract {
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
func (f *Fields) set(name, value string) {
	switch name {
	case config.ExtractAlert:
		if f.Alert == "" {
			f.Alert = value
		}
	case config.ExtractService:
		if f.Service == "" {
			f.Service = value
		}
	case config.ExtractSeverity:
		if f.Severity == event.SeverityUnknown {
			f.Severity = intake.ParseSeverity(value)
		}
	default:
		if f.Labels == nil {
			f.Labels = map[string]string{}
		}

		if _, ok := f.Labels[name]; !ok {
			f.Labels[name] = value
		}
	}
}

// IsRecovery reports whether text matches the listener's recovery pattern.
func (r Rules) IsRecovery(text string) bool {
	return r.recovery != nil && r.recovery.MatchString(text)
}

// CompileAll compiles the configured listeners that watch source, in name
// order, which is the order messages are matched in.
func CompileAll(cfg map[string]config.Listener, source config.ListenerSource) ([]Rules, error) {
	out := make([]Rules, 0, len(cfg))

	for _, name := range slices.Sorted(maps.Keys(cfg)) {
		c := cfg[name]
		if c.OnTeams() != (source == config.ListenerSourceTeams) {
			continue
		}

		r, err := Compile(name, c)
		if err != nil {
			return nil, fmt.Errorf("%s listeners: %w", source, err)
		}

		out = append(out, r)
	}

	return out, nil
}

// Compile compiles the listener named name.
func Compile(name string, c config.Listener) (Rules, error) {
	r := Rules{Name: name, Channel: c.Channel, Sender: c.Match.Sender, Trusted: c.Trusted}

	var err error
	if r.text, err = optionalRegexp(c.Match.Text); err != nil {
		return Rules{}, fmt.Errorf("listener %s: match.text: %w", name, err)
	}

	if r.recovery, err = optionalRegexp(c.Recovery); err != nil {
		return Rules{}, fmt.Errorf("listener %s: recovery: %w", name, err)
	}

	for i, pattern := range c.Extract {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Rules{}, fmt.Errorf("listener %s: extract[%d]: %w", name, i, err)
		}

		r.extract = append(r.extract, re)
	}

	return r, nil
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

// Title returns the first line of a message's text that is not blank, cut
// to MaxTitleBytes, as the title of its event.
func Title(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return source.CutText(line, MaxTitleBytes)
		}
	}

	return ""
}

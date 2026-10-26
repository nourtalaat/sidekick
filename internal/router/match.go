package router

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// matcher is a route's match, ready to run. The zero value matches every
// event, as a route without a match does.
type matcher struct {
	intakes    []string
	labels     map[string]string
	service    string
	severities []event.Severity
	text       *regexp.Regexp
}

// candidate is what routes match on, taken from a stored event.
type candidate struct {
	intake   string
	labels   map[string]string
	service  string
	severity event.Severity
	// text is the title and the body, joined by a newline.
	text string
}

// newMatcher compiles m. A nil m matches every event.
func newMatcher(m *config.Match) (matcher, error) {
	if m == nil {
		return matcher{}, nil
	}

	out := matcher{intakes: m.Intake, labels: m.Labels, service: m.Service, severities: m.Severity}

	if m.Service != "" {
		if _, err := path.Match(m.Service, ""); err != nil {
			return matcher{}, fmt.Errorf("service: invalid glob %q: %w", m.Service, err)
		}
	}

	if m.Text != "" {
		re, err := regexp.Compile(m.Text)
		if err != nil {
			return matcher{}, fmt.Errorf("text: %w", err)
		}

		out.text = re
	}

	return out, nil
}

// matches reports whether every field the match sets matches c.
func (m matcher) matches(c candidate) bool {
	if len(m.intakes) > 0 && !slices.Contains(m.intakes, c.intake) {
		return false
	}

	for name, want := range m.labels {
		if got, ok := c.labels[name]; !ok || got != want {
			return false
		}
	}

	if m.service != "" {
		if ok, err := path.Match(m.service, c.service); err != nil || !ok {
			return false
		}
	}

	if len(m.severities) > 0 && !slices.Contains(m.severities, c.severity) {
		return false
	}

	return m.text == nil || m.text.MatchString(c.text)
}

// candidateOf takes the fields routes match on from a stored event, which
// intake has already redacted.
func candidateOf(ev db.Event) (candidate, error) {
	labels := map[string]string{}
	if len(ev.Labels) > 0 {
		if err := json.Unmarshal(ev.Labels, &labels); err != nil {
			return candidate{}, fmt.Errorf("decode the labels: %w", err)
		}
	}

	var text []string

	for _, s := range []*string{ev.Title, ev.Body} {
		if s != nil && *s != "" {
			text = append(text, *s)
		}
	}

	return candidate{
		intake:   ev.Intake,
		labels:   labels,
		service:  deref(ev.Service),
		severity: event.Severity(deref(ev.Severity)),
		text:     strings.Join(text, "\n"),
	}, nil
}

// deref returns the text s points to, or "" for NULL.
func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

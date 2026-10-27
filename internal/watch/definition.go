package watch

import (
	"errors"
	"fmt"
	"path"
	"regexp"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/jsonpath"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// Origin says what started a watcher.
type Origin string

// Origins.
const (
	OriginRoute    Origin = "route"
	OriginStrategy Origin = "strategy"
	OriginModel    Origin = "model"
)

// NamePattern is the rule for watcher names: like strategy names, so a
// strategy's watcher names fit too.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// NameRule explains NamePattern in error messages.
const NameRule = `must start with a lower-case letter and hold only lower-case letters, digits and "-", at most 64 characters`

var errNotScalar = errors.New("must be a string, a number or a boolean")

// Definition is a watcher that a route or a strategy defines, ready to
// start when a group of the route opens: its check, and its settings
// resolved from every level of defaults.
type Definition struct {
	Name     string
	Origin   Origin
	Match    Match
	Check    Check
	Settings Settings
}

// Match selects the events a definition starts for. The zero value
// matches every event.
type Match struct {
	// AlertNames are globs in path.Match syntax; one must match the alert
	// name when any are set.
	AlertNames []string
	// Text must match the event text when set.
	Text *regexp.Regexp
}

// Matches reports whether an event with this alert name and text matches.
func (m Match) Matches(alertName, text string) bool {
	if len(m.AlertNames) > 0 && !matchesAny(m.AlertNames, alertName) {
		return false
	}

	return m.Text == nil || m.Text.MatchString(text)
}

// MatchOf compiles the match of a watcher definition.
func MatchOf(m strategy.WatcherMatch) (Match, error) {
	for i, g := range m.AlertName {
		if _, err := path.Match(g, ""); err != nil {
			return Match{}, fmt.Errorf("alertName[%d]: invalid glob %q: %w", i, g, err)
		}
	}

	out := Match{AlertNames: m.AlertName}

	if m.Text != "" {
		re, err := regexp.Compile(m.Text)
		if err != nil {
			return Match{}, fmt.Errorf("text: invalid regular expression: %w", err)
		}

		out.Text = re
	}

	return out, nil
}

// CheckOf turns the check of a watcher definition, as a route or a
// strategy file writes it, into a Check. It does not run Validate.
func CheckOf(c strategy.WatcherCheck) (Check, error) {
	switch Type(c.Type) {
	case TypeHTTP:
		return httpCheckOf(c), nil
	case TypeTool:
		return toolCheckOf(c)
	case TypeAlert:
		return AlertCheck{Require: Require(c.Require)}, nil
	default:
		return nil, fmt.Errorf("type: expected one of http, tool or alert, got %q", c.Type)
	}
}

// httpCheckOf returns the HTTP check of a definition.
func httpCheckOf(c strategy.WatcherCheck) HTTPCheck {
	h := HTTPCheck{URL: c.URL, Timeout: c.Timeout}

	if c.Expect != nil {
		h.Expect = Expect{Status: c.Expect.Status, BodyRegex: c.Expect.BodyRegex}
	}

	if len(c.Headers) > 0 {
		h.Headers = make(map[string]registry.SecretRef, len(c.Headers))
		for name, ref := range c.Headers {
			h.Headers[name] = registry.SecretRef{Name: ref.Name, Key: ref.Key}
		}
	}

	return h
}

// toolCheckOf returns the tool check of a definition.
func toolCheckOf(c strategy.WatcherCheck) (Check, error) {
	t := ToolCheck{Source: c.Source, Tool: c.Tool, NoData: NoData(c.NoData)}

	if !c.Args.IsZero() {
		if err := c.Args.Decode(&t.Args); err != nil {
			return nil, fmt.Errorf("args: expected a mapping: %w", err)
		}
	}

	if c.Condition != nil {
		cond, err := conditionOf(*c.Condition)
		if err != nil {
			return nil, fmt.Errorf("condition: %w", err)
		}

		t.Condition = cond
	}

	return t, nil
}

// conditionOf returns the condition of a tool check definition.
func conditionOf(c strategy.Condition) (Condition, error) {
	out := Condition{Op: Op(c.Op), TextRegex: c.TextRegex, Negate: c.Negate}

	if c.JSONPath != "" {
		p, err := jsonpath.Parse(c.JSONPath)
		if err != nil {
			return Condition{}, fmt.Errorf("jsonPath: %w", err)
		}

		out.JSONPath = p
	}

	switch c.Value.Kind {
	case 0:
	case yaml.ScalarNode:
		out.Value = Scalar(c.Value.Value)
	default:
		return Condition{}, fmt.Errorf("value: %w", errNotScalar)
	}

	return out, nil
}

// matchesAny reports whether any glob matches name.
func matchesAny(globs []string, name string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}

	return false
}

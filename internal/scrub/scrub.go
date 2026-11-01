// Package scrub cleans a benchmark scenario draft before people review it.
//
// It is a stricter pass than the redaction that runs when data is stored.
// It learns every email, user name, account ID, host name and IP address
// in the whole draft, in URLs, JSON, log lines and key-value pairs alike,
// and replaces each one, everywhere it appears, with a numbered
// placeholder such as host-1 or user-2. The same value always gets the
// same placeholder, in the event, the fixtures and the expected outcome.
// Every built-in redaction rule then runs over the result, and a last
// check fails when anything that looks like an email, a host name or an IP
// address is left.
//
// A value that cannot be told from an ordinary word is kept. Opaque IDs
// and the owner part of code host URLs are reported as candidates for the
// reviewer instead of being guessed at.
package scrub

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/redact"
)

// Options configure a Cleaner.
type Options struct {
	// HashKey keys the HMAC of the redaction placeholders, as in redact.
	HashKey []byte
	// Custom are the deployment's own redaction rules.
	Custom []redact.Rule
	// Logger receives the redactor's warning about a missing key.
	Logger *slog.Logger
}

// Cleaner cleans drafts. It holds no state about a draft, so it is safe for
// concurrent use; NewSession holds the state.
type Cleaner struct {
	// finder reports where emails and IP addresses are.
	finder *redact.Redactor
	// strict has every built-in rule, IP addresses included.
	strict *redact.Redactor
}

// Candidate is a place the cleaner could not classify and the reviewer
// should look at. It holds no value.
type Candidate struct {
	// Path is the field, for example "sources.signoz.fixtures[0].respond.text".
	Path string
	// Start and End are byte offsets into the cleaned text of the field.
	Start, End int
	Reason     string
}

// Leak is a field that still holds something that looks sensitive.
type Leak struct {
	Path string
	// Kind is email, host or ip.
	Kind string
}

// LeakError is returned when cleaned or edited text still holds an email, a
// host name or an IP address. It lists where, never the values.
type LeakError struct {
	Leaks []Leak
}

// Error lists the places.
func (e *LeakError) Error() string {
	parts := make([]string, len(e.Leaks))
	for i, l := range e.Leaks {
		parts[i] = fmt.Sprintf("%s (%s)", l.Path, l.Kind)
	}

	return "the scenario still holds values that look like emails, host names or IP addresses: " + strings.Join(parts, ", ")
}

// New builds a Cleaner.
func New(opts Options) (*Cleaner, error) {
	finder, err := redact.New(redact.Options{BuiltIn: []string{redact.Emails, redact.IPAddresses}, HashKey: opts.HashKey, Logger: opts.Logger})
	if err != nil {
		return nil, fmt.Errorf("build the finder: %w", err)
	}

	strict, err := redact.New(redact.Options{BuiltIn: redact.BuiltInRuleNames(), Custom: opts.Custom, HashKey: opts.HashKey, Logger: opts.Logger})
	if err != nil {
		return nil, fmt.Errorf("build the strict redactor: %w", err)
	}

	return &Cleaner{finder: finder, strict: strict}, nil
}

// NewSession starts the cleaning of one draft. A session numbers its
// placeholders from 1; use one session for every part of a draft so that
// a placeholder never means two values.
func (c *Cleaner) NewSession() *Session {
	return &Session{c: c, entities: map[entityKey]*entity{}, counts: map[Kind]int{}}
}

// Check reports the emails, host names and IP addresses left in doc, and
// changes nothing. Use it on text people edited.
func (c *Cleaner) Check(doc *yaml.Node) error {
	var leaks []Leak

	walk(doc, func(v scalar) {
		for _, kind := range c.leaks(v.node.Value) {
			leaks = append(leaks, Leak{Path: v.path, Kind: kind})
		}
	})

	if len(leaks) == 0 {
		return nil
	}

	sort.SliceStable(leaks, func(i, j int) bool { return leaks[i].Path < leaks[j].Path })

	return &LeakError{Leaks: leaks}
}

// Candidates returns the values in doc that look like IDs the cleaner kept.
func (c *Cleaner) Candidates(doc *yaml.Node) []Candidate {
	var out []Candidate

	walk(doc, func(v scalar) {
		if v.key {
			return
		}

		out = append(out, candidatesIn(v.path, v.node.Value)...)
	})

	return out
}

// Session is the state of one draft's cleaning: the values learned so far
// and their placeholders.
type Session struct {
	c        *Cleaner
	entities map[entityKey]*entity
	order    []*entity
	counts   map[Kind]int

	// matcher is built from entities when a replacement needs it, and set
	// to nil when a new value is learned.
	matcher *matcher
}

// Clean learns the values in doc, replaces them in place, runs every
// built-in redaction rule over the result and checks that nothing sensitive
// is left. It returns *LeakError when something is. It may be called again
// with more text of the same draft.
func (s *Session) Clean(doc *yaml.Node) ([]Candidate, error) {
	walk(doc, func(v scalar) { s.learnScalar(v) })

	walk(doc, func(v scalar) {
		cleaned := s.c.strict.Text(s.replace(v.node.Value)).Text

		if cleaned != v.node.Value && v.node.Tag != "!!str" {
			v.node.Tag = "!!str"
			v.node.Style = 0
		}

		v.node.Value = cleaned
	})

	if err := s.c.Check(doc); err != nil {
		return nil, fmt.Errorf("check the cleaned text: %w", err)
	}

	return s.c.Candidates(doc), nil
}

// CleanText cleans one string with the values this session has learned
// and any it finds in the string. It is for text such as a model's answer
// that is cleaned after the document.
func (s *Session) CleanText(text string) (string, error) {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: text}

	if _, err := s.Clean(node); err != nil {
		return "", fmt.Errorf("clean text: %w", err)
	}

	return node.Value, nil
}

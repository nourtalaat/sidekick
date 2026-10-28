package flags

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the text a result keeps, in bytes.
const (
	// MaxResultSize bounds the JSON of the changes of one result. A list
	// stops adding changes once it reaches it and says it was cut.
	MaxResultSize = 128 << 10
	// MaxTextLen bounds one change summary or comment.
	MaxTextLen = 2 << 10
	// MaxNameLen bounds a key, name, action, actor or variation.
	MaxNameLen = 256
	// maxURLLen bounds a link. A longer one is left out rather than cut,
	// since a cut link does not open.
	maxURLLen = 2 << 10
)

// Changes is the result of list_flag_changes.
type Changes struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	// Flag is the flag the call asked about, when it named one.
	Flag    string    `json:"flag,omitempty"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Changes []Change  `json:"changes"`
}

// Change is one change to a flag, or to something a flag depends on, in a
// flag service's audit or event log.
type Change struct {
	Time time.Time `json:"time"`
	// Flag is the key of the changed flag, when the change is to one.
	Flag string `json:"flag,omitempty"`
	// Name is the name of the changed resource, such as the flag's name.
	Name string `json:"name,omitempty"`
	// Action says what was done, such as "turned on the flag".
	Action string `json:"action,omitempty"`
	// Actor is who or what made the change: a member's email, an access
	// token or an integration.
	Actor string `json:"actor,omitempty"`
	// Summary lists what changed. It comes from the flag service and is
	// untrusted.
	Summary string `json:"summary,omitempty"`
	// Comment is the comment the actor left with the change. It is
	// untrusted.
	Comment string `json:"comment,omitempty"`
	// URL links to the change, or to what it changed, in the flag service.
	URL string `json:"url,omitempty"`
}

// State is the result of get_flag: one flag's current state in one
// environment.
type State struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Flag        string `json:"flag"`
	Name        string `json:"name,omitempty"`
	// Kind is the flag's type, such as boolean or multivariate.
	Kind string `json:"kind,omitempty"`
	// On says whether the flag is on in the environment.
	On       bool `json:"on"`
	Archived bool `json:"archived,omitempty"`
	// Rules is the number of targeting rules, or strategies, in the
	// environment.
	Rules int `json:"rules"`
	// Targets is the number of users or contexts the flag targets one by
	// one in the environment.
	Targets int `json:"targets,omitempty"`
	// Prerequisites is the number of flags this flag depends on.
	Prerequisites int `json:"prerequisites,omitempty"`
	// Fallthrough is what the flag serves when it is on and no rule or
	// target matches: a variation, a percentage rollout, or a default
	// strategy.
	Fallthrough string `json:"fallthrough,omitempty"`
	// OffVariation is what the flag serves when it is off.
	OffVariation string `json:"offVariation,omitempty"`
	// LastChanged is when the flag last changed in the environment.
	LastChanged time.Time `json:"lastChanged,omitzero"`
	// Version is the flag's version in the environment, which grows with
	// each change.
	Version int    `json:"version,omitempty"`
	URL     string `json:"url,omitempty"`
}

// ChangeList collects the changes of a list_flag_changes result, cutting
// long text and stopping before the result grows past MaxResultSize.
type ChangeList struct {
	out  Changes
	used int
	// full says a change did not fit, so the list takes no more.
	full bool
	// cut says the text of a change was cut.
	cut bool
}

// Add cuts the text of c and adds it to the list. It reports false, and
// adds nothing, once the list is full; the result then says it was cut.
func (l *ChangeList) Add(c Change) bool {
	if l.full {
		return false
	}

	c, cut := c.cutText()

	data, err := json.Marshal(c)
	if err != nil || l.used+len(data) > MaxResultSize {
		l.full = true

		return false
	}

	l.used += len(data)
	l.out.Changes = append(l.out.Changes, c)
	l.cut = l.cut || cut

	return true
}

// Len returns the number of changes in the list.
func (l *ChangeList) Len() int {
	return len(l.out.Changes)
}

// Result returns the list as a tool result with its evidence. more says
// the service has changes the list did not read, such as past the call's
// limit. An empty list is a result with NoData set, with a text that
// names the range and the evidence, so the link still reaches the report.
func (l *ChangeList) Result(service string, more bool, evidence []source.Evidence) (source.Result, error) {
	if len(evidence) == 0 {
		evidence = nil
	}

	if len(l.out.Changes) == 0 {
		return source.Result{NoData: true, Text: l.emptyText(service), Evidence: evidence}, nil
	}

	data, err := json.Marshal(l.out)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: more || l.full || l.cut}, nil
}

// Result returns the state as a tool result with its evidence, with long
// names cut.
func (s State) Result(evidence []source.Evidence) (source.Result, error) {
	if len(evidence) == 0 {
		evidence = nil
	}

	s, cut := s.cutText()

	data, err := json.Marshal(s)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: cut}, nil
}

// emptyText returns the text of a result without changes.
func (l *ChangeList) emptyText(service string) string {
	what := "No flag changes"
	if l.out.Flag != "" {
		what = "No changes to flag " + l.out.Flag
	}

	return fmt.Sprintf("%s in %s project %s, environment %s, between %s and %s.", what, service, l.out.Project,
		l.out.Environment, timestamp(l.out.Since), timestamp(l.out.Until))
}

// cutText returns c with each text field cut to its limit, and reports
// whether one was cut.
func (c Change) cutText() (Change, bool) {
	var t cutter

	c.Flag = t.cut(c.Flag, MaxNameLen)
	c.Name = t.cut(c.Name, MaxNameLen)
	c.Action = t.cut(c.Action, MaxNameLen)
	c.Actor = t.cut(c.Actor, MaxNameLen)
	c.Summary = t.cut(c.Summary, MaxTextLen)
	c.Comment = t.cut(c.Comment, MaxTextLen)
	c.URL = t.link(c.URL)

	return c, t.cutAny
}

// cutText returns s with each text field cut to its limit, and reports
// whether one was cut.
func (s State) cutText() (State, bool) {
	var t cutter

	s.Flag = t.cut(s.Flag, MaxNameLen)
	s.Name = t.cut(s.Name, MaxNameLen)
	s.Kind = t.cut(s.Kind, MaxNameLen)
	s.Fallthrough = t.cut(s.Fallthrough, MaxTextLen)
	s.OffVariation = t.cut(s.OffVariation, MaxNameLen)
	s.URL = t.link(s.URL)

	return s, t.cutAny
}

// cutter cuts text and remembers whether it cut any.
type cutter struct {
	cutAny bool
}

// cut returns s cut to n bytes.
func (t *cutter) cut(s string, n int) string {
	if len(s) > n {
		t.cutAny = true

		return source.CutText(s, n)
	}

	return s
}

// link returns u, or nothing when it is longer than maxURLLen.
func (t *cutter) link(u string) string {
	if len(u) > maxURLLen {
		t.cutAny = true

		return ""
	}

	return u
}

// NewChangeList returns an empty list for a call that reads scope in w,
// for flag when the call names one.
func NewChangeList(scope Scope, flag string, w Window) *ChangeList {
	return &ChangeList{out: Changes{
		Project:     scope.Project,
		Environment: scope.Environment,
		Flag:        flag,
		Since:       w.Start,
		Until:       w.End,
		Changes:     []Change{},
	}}
}

// timestamp formats a time for a result text.
func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

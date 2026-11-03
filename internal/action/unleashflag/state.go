package unleashflag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The kinds of step a change is made of.
const (
	stepOff stepKind = iota + 1
	stepOn
)

var errManySteps = errors.New("the snapshot changes more than one thing, which Sidekick never writes")

// state is what the action reads and writes of a flag in one environment:
// whether it is on, and its strategies by ID. A strategy that does not
// exist yet has no ID.
type state struct {
	Enabled    bool                `json:"enabled"`
	Strategies map[string]strategy `json:"strategies"`
}

// strategy is the configuration of one strategy of a flag in one
// environment, without its ID, as Unleash writes it. The raw fields are in
// a fixed form, so two strategies that hold the same values hold the same
// text.
type strategy struct {
	Name        string          `json:"name"`
	Title       *string         `json:"title"`
	Disabled    bool            `json:"disabled"`
	SortOrder   float64         `json:"sortOrder"`
	Constraints json.RawMessage `json:"constraints"`
	Parameters  json.RawMessage `json:"parameters"`
	Variants    json.RawMessage `json:"variants"`
	Segments    json.RawMessage `json:"segments"`
}

// liveStrategy is a strategy as Unleash lists it.
type liveStrategy struct {
	ID string `json:"id"`
	strategy
}

// stepKind says which call a step makes.
type stepKind int

// step is the one call a change makes.
type step struct {
	kind stepKind
}

// newState makes the state of a flag from what Unleash lists.
func newState(enabled bool, live []liveStrategy) (state, error) {
	st := state{Enabled: enabled, Strategies: make(map[string]strategy, len(live))}

	for _, l := range live {
		s, err := l.strategy.normalised()
		if err != nil {
			return state{}, fmt.Errorf("read strategy %s: %w", l.ID, err)
		}

		st.Strategies[l.ID] = s
	}

	return st, nil
}

// diffStep returns the one call that turns before into after. A snapshot
// of Sidekick holds exactly one change.
func diffStep(before, after state) (step, error) {
	if before.Enabled != after.Enabled && before.sameStrategies(after) {
		if after.Enabled {
			return step{kind: stepOn}, nil
		}

		return step{kind: stepOff}, nil
	}

	return step{}, errManySteps
}

// atAfter reports whether the live state already has what the step writes.
func (s step) atAfter(live state) bool {
	return live.Enabled == (s.kind == stepOn)
}

// verb says what the step does to the flag, as "on" or "off".
func (s step) verb() string {
	if s.kind == stepOn {
		return "on"
	}

	return "off"
}

// same reports whether two states hold the same values, strategy by
// strategy, with the same IDs.
func (s state) same(other state) bool {
	return s.Enabled == other.Enabled && s.sameStrategies(other)
}

// sameStrategies reports whether two states hold the same strategies, with
// the same IDs.
func (s state) sameStrategies(other state) bool {
	if len(s.Strategies) != len(other.Strategies) {
		return false
	}

	for id, mine := range s.Strategies {
		theirs, ok := other.Strategies[id]
		if !ok || !mine.equal(theirs) {
			return false
		}
	}

	return true
}

// normalised returns the strategy with every raw field in its fixed form:
// a missing or null list or object is empty, and objects have sorted keys.
func (s strategy) normalised() (strategy, error) {
	var err error

	if s.Constraints, err = fixedForm(s.Constraints, "[]"); err != nil {
		return strategy{}, fmt.Errorf("read constraints: %w", err)
	}

	if s.Parameters, err = fixedForm(s.Parameters, "{}"); err != nil {
		return strategy{}, fmt.Errorf("read parameters: %w", err)
	}

	if s.Variants, err = fixedForm(s.Variants, "[]"); err != nil {
		return strategy{}, fmt.Errorf("read variants: %w", err)
	}

	if s.Segments, err = fixedForm(s.Segments, "[]"); err != nil {
		return strategy{}, fmt.Errorf("read segments: %w", err)
	}

	return s, nil
}

// equal reports whether two strategies hold the same values, with numbers
// compared as numbers.
func (s strategy) equal(other strategy) bool {
	a, errA := json.Marshal(s)
	b, errB := json.Marshal(other)

	if errA != nil || errB != nil {
		return false
	}

	deltas, err := action.Diff(a, b)

	return err == nil && len(deltas) == 0
}

// fixedForm returns raw with its object keys sorted, or empty when raw is
// missing or null.
func fixedForm(raw json.RawMessage, empty string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return json.RawMessage(empty), nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return out, nil
}

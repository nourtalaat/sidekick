package unleashflag

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The kinds of step a change is made of.
const (
	stepOff stepKind = iota + 1
	stepOn
	stepCreate
	stepUpdate
	stepDelete
)

// newStrategyKey is the key of a strategy that does not exist yet in the
// strategies of a state. Unleash gives the strategy its ID when it is made.
const newStrategyKey = "new"

var errNotOneStep = errors.New("the snapshot does not hold exactly one change, which is all Sidekick writes")

// state is what the action reads and writes of a flag in one environment:
// whether it is on, and its strategies by ID.
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

// liveStrategy is a strategy as Unleash lists it. A strategy of a release
// plan has a milestone.
type liveStrategy struct {
	ID          string  `json:"id"`
	MilestoneID *string `json:"milestoneId"`
	strategy
}

// stepKind says which call a step makes.
type stepKind int

// step is the one call a change makes. A step that updates or deletes a
// strategy holds its ID and the strategy as it is before. A step that
// creates or updates one holds the strategy as it is after.
type step struct {
	kind stepKind
	id   string
	from strategy
	to   strategy
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
	if before.Enabled != after.Enabled {
		if !before.sameStrategies(after) {
			return step{}, errNotOneStep
		}

		if after.Enabled {
			return step{kind: stepOn}, nil
		}

		return step{kind: stepOff}, nil
	}

	var steps []step

	for _, id := range slices.Sorted(maps.Keys(before.Strategies)) {
		was := before.Strategies[id]

		switch now, ok := after.Strategies[id]; {
		case !ok:
			steps = append(steps, step{kind: stepDelete, id: id, from: was})
		case !was.equal(now):
			steps = append(steps, step{kind: stepUpdate, id: id, from: was, to: now})
		}
	}

	for _, id := range slices.Sorted(maps.Keys(after.Strategies)) {
		if _, ok := before.Strategies[id]; !ok {
			steps = append(steps, step{kind: stepCreate, id: id, to: after.Strategies[id]})
		}
	}

	if len(steps) != 1 {
		return step{}, errNotOneStep
	}

	return steps[0], nil
}

// atAfter reports whether the live state already has what the step writes.
func (s step) atAfter(live state) bool {
	switch s.kind {
	case stepOff:
		return !live.Enabled
	case stepOn:
		return live.Enabled
	case stepCreate:
		_, ok := live.idOf(s.to)

		return ok
	case stepUpdate:
		now, ok := live.Strategies[s.id]

		return ok && now.equal(s.to)
	case stepDelete:
		_, ok := live.Strategies[s.id]

		return !ok
	default:
		return false
	}
}

// summary says what the step did, for a result.
func (s step) summary(p Params) string {
	where := fmt.Sprintf("in %s of project %s", p.Environment, p.Project)

	switch s.kind {
	case stepOff:
		return fmt.Sprintf("Turned flag %s off %s.", p.Flag, where)
	case stepOn:
		return fmt.Sprintf("Turned flag %s on %s.", p.Flag, where)
	case stepCreate:
		return fmt.Sprintf("Added a strategy to flag %s %s.", p.Flag, where)
	case stepUpdate:
		return fmt.Sprintf("Changed strategy %s of flag %s %s.", s.id, p.Flag, where)
	case stepDelete:
		return fmt.Sprintf("Removed strategy %s from flag %s %s.", s.id, p.Flag, where)
	default:
		return fmt.Sprintf("Changed flag %s %s.", p.Flag, where)
	}
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

// idOf returns the ID of a strategy that holds the same values as want.
func (s state) idOf(want strategy) (string, bool) {
	for _, id := range slices.Sorted(maps.Keys(s.Strategies)) {
		if s.Strategies[id].equal(want) {
			return id, true
		}
	}

	return "", false
}

// hasEnabledStrategy reports whether the state has a strategy that is not
// disabled. Unleash turns an environment off by itself when its strategies
// are all disabled, and adds a default strategy when it is turned on with
// none that is enabled.
func (s state) hasEnabledStrategy() bool {
	for _, st := range s.Strategies {
		if !st.Disabled {
			return true
		}
	}

	return false
}

// cloned returns a copy of the state that can change without changing it.
func (s state) cloned() state {
	return state{Enabled: s.Enabled, Strategies: maps.Clone(s.Strategies)}
}

// withStrategy returns a copy of the state in which the strategy with id is
// st.
func (s state) withStrategy(id string, st strategy) state {
	out := s.cloned()
	out.Strategies[id] = st

	return out
}

// withoutStrategy returns a copy of the state without the strategy with id.
func (s state) withoutStrategy(id string) state {
	out := s.cloned()
	delete(out.Strategies, id)

	return out
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

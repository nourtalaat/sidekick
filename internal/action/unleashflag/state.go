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
		s, err := l.normalised()
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

// undoPlan says where the flag stands with respect to a change Sidekick made,
// and what puts it back.
type undoPlan struct {
	// restored is true when the flag has what it had before the change.
	restored bool
	// asWritten is true when the part of the flag the change wrote still has
	// what the change wrote.
	asWritten bool
	// back is the step that puts the change back.
	back step
	// err says why no step can put it back, when none can.
	err error
}

var (
	errStrategyGone = errors.New("the strategy the change wrote is not in this environment any more")
	errStrategyMany = errors.New("this environment has more than one new strategy that could be the one the change added, so Sidekick cannot tell which to remove")
)

// planUndo says where the live state stands with respect to the change made,
// and which step puts it back. before is the state before the change; the
// strategies it holds keep their IDs.
func planUndo(made step, live, before state) undoPlan {
	switch made.kind {
	case stepOff:
		return undoPlan{restored: live.Enabled, asWritten: !live.Enabled, back: step{kind: stepOn}}
	case stepOn:
		return undoPlan{restored: !live.Enabled, asWritten: live.Enabled, back: step{kind: stepOff}}
	case stepUpdate:
		now, ok := live.Strategies[made.id]
		if !ok {
			return undoPlan{err: errStrategyGone}
		}

		return undoPlan{
			restored:  now.equal(made.from),
			asWritten: now.equal(made.to),
			back:      step{kind: stepUpdate, id: made.id, from: now, to: made.from},
		}
	case stepDelete:
		_, backAgain := live.idOf(made.from)
		_, present := live.Strategies[made.id]

		return undoPlan{restored: backAgain, asWritten: !present, back: step{kind: stepCreate, id: newStrategyKey, to: made.from}}
	case stepCreate:
		return planUndoCreate(made, live, before)
	default:
		return undoPlan{err: errNotOneStep}
	}
}

// planUndoCreate plans the undo of a change that added a strategy. Unleash
// gave the strategy an ID the snapshot does not hold, so the strategy is
// one of the strategies the live state has and the state before the change
// had not: the only one, or else the only one that holds the values the
// change wrote.
func planUndoCreate(made step, live, before state) undoPlan {
	var added []string

	for _, id := range slices.Sorted(maps.Keys(live.Strategies)) {
		if _, ok := before.Strategies[id]; !ok {
			added = append(added, id)
		}
	}

	plan := undoPlan{restored: len(added) == 0}

	var matching []string

	for _, id := range added {
		if live.Strategies[id].equal(made.to) {
			matching = append(matching, id)
		}
	}

	plan.asWritten = len(matching) > 0

	switch {
	case len(added) == 1:
		plan.back = step{kind: stepDelete, id: added[0], from: live.Strategies[added[0]]}
	case len(matching) == 1:
		plan.back = step{kind: stepDelete, id: matching[0], from: live.Strategies[matching[0]]}
	case len(added) > 1:
		plan.err = errStrategyMany
	}

	return plan
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

package unleashflag

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// The types of Unleash event that restore_previous can undo.
const (
	eventEnabled        = "feature-environment-enabled"
	eventDisabled       = "feature-environment-disabled"
	eventStrategyAdd    = "feature-strategy-add"
	eventStrategyUpdate = "feature-strategy-update"
	eventStrategyRemove = "feature-strategy-remove"
)

// maxEventTypeBytes is the longest event type written into a refusal. The
// type comes from Unleash.
const maxEventTypeBytes = 64

// restorable lists the changes restore_previous can undo, for a refusal and
// for the description the model reads.
const restorable = "turning the flag on or off in the environment, and adding, changing or removing one strategy"

var errNoEventData = errors.New("the event holds no strategy")

// eventStrategy is the strategy an event of a strategy holds.
type eventStrategy struct {
	ID string `json:"id"`
	strategy
}

// restoreTarget returns the state the flag has once the newest change of
// the environment is undone, which is the state before it. live is the
// state now, and newest the event of that change. It is a refusal that
// wraps action.ErrLimit when Sidekick cannot undo it: there is no change,
// it is not one of the changes the action restores, or undoing it would
// leave the flag in a state Unleash changes by itself.
func restoreTarget(live state, newest event) (state, error) {
	if newest.ID == 0 {
		return state{}, refusal("the flag has no change in this environment to restore")
	}

	if newest.GroupID != "" {
		return state{}, refusal("the newest change of the flag in this environment is one of a group of changes made together, " +
			"such as a change request; Sidekick restores one change at a time, so restore it in Unleash")
	}

	after, err := undoEvent(live, newest)
	if err != nil {
		return state{}, fmt.Errorf("undo the newest change: %w", err)
	}

	if after.Enabled && !after.hasEnabledStrategy() {
		return state{}, refusal("restoring the previous state would leave the flag on with no enabled strategy, " +
			"and Unleash then turns it off, or adds a default strategy, by itself; use turn_off instead, or restore it in Unleash")
	}

	return after, nil
}

// undoEvent returns the state that undoes one event of live.
func undoEvent(live state, newest event) (state, error) {
	switch newest.Type {
	case eventEnabled:
		if !live.Enabled {
			return state{}, refusal("the newest change turned the flag on, and it is already off in this environment; there is nothing to restore")
		}

		after := live.cloned()
		after.Enabled = false

		return after, nil
	case eventDisabled:
		if live.Enabled {
			return state{}, refusal("the newest change turned the flag off, and it is already on in this environment; there is nothing to restore")
		}

		after := live.cloned()
		after.Enabled = true

		return after, nil
	case eventStrategyUpdate:
		return undoUpdate(live, newest)
	case eventStrategyAdd:
		return undoAdd(live, newest)
	case eventStrategyRemove:
		return undoRemove(live, newest)
	default:
		return state{}, refusal("the newest change of the flag in this environment is of type %q, which Sidekick cannot restore; "+
			"it restores only %s, and a reordering of the strategies is not one of them",
			source.CutText(newest.Type, maxEventTypeBytes), restorable)
	}
}

// undoUpdate puts the strategy an event changed back to its values from
// before.
func undoUpdate(live state, newest event) (state, error) {
	id, before, err := strategyOf(newest.PreData)
	if err != nil {
		return state{}, refusal("the newest change updated a strategy, but Unleash gives no strategy for it before the change: %v", err)
	}

	now, ok := live.Strategies[id]
	if !ok {
		return state{}, refusal("the strategy the newest change updated is not in this environment any more")
	}

	if now.equal(before) {
		return state{}, refusal("the strategy the newest change updated already has the values it had before; there is nothing to restore")
	}

	return live.withStrategy(id, before), nil
}

// undoAdd removes the strategy an event added.
func undoAdd(live state, newest event) (state, error) {
	id, _, err := strategyOf(newest.Data)
	if err != nil {
		return state{}, refusal("the newest change added a strategy, but Unleash gives no strategy for it: %v", err)
	}

	if _, ok := live.Strategies[id]; !ok {
		return state{}, refusal("the strategy the newest change added is not in this environment any more; there is nothing to restore")
	}

	return live.withoutStrategy(id), nil
}

// undoRemove adds back the strategy an event removed. It gets a new ID.
func undoRemove(live state, newest event) (state, error) {
	_, before, err := strategyOf(newest.PreData)
	if err != nil {
		return state{}, refusal("the newest change removed a strategy, but Unleash gives no strategy for it: %v", err)
	}

	if _, ok := live.idOf(before); ok {
		return state{}, refusal("this environment already has a strategy with the values of the one the newest change removed; there is nothing to restore")
	}

	return live.withStrategy(newStrategyKey, before), nil
}

// strategyOf reads the strategy an event holds, with its ID, in its fixed
// form.
func strategyOf(raw json.RawMessage) (string, strategy, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return "", strategy{}, errNoEventData
	}

	var held eventStrategy
	if err := json.Unmarshal(raw, &held); err != nil {
		return "", strategy{}, fmt.Errorf("decode the strategy: %w", err)
	}

	if held.ID == "" {
		return "", strategy{}, errNoEventData
	}

	st, err := held.normalised()
	if err != nil {
		return "", strategy{}, fmt.Errorf("read the strategy: %w", err)
	}

	return held.ID, st, nil
}

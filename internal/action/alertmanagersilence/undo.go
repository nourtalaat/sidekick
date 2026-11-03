package alertmanagersilence

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// endTolerance is how far the length of a live silence may differ from the
// duration that was written before Undo calls it edited. Alertmanager moves
// a start that is in its past to its own now, so a clock that runs a little
// ahead of Sidekick's makes the silence a little shorter.
const endTolerance = time.Minute

// Undo implements action.Undoer: it ends the silence early. The silence is
// found by the marker of the proposal that made it, because a person who
// edits a silence in Alertmanager may change its ID. A silence that has
// ended already, or that is gone, counts as success. When a silence that is
// still running no longer has the matchers or the length that were written,
// someone edited it, and unless force is set Undo ends nothing and returns
// an *action.ConflictError with the live silence. Ending a silence is the
// safe direction, so the limits are not checked again.
func (s *silencer) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	if inv.UndoOf == "" {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoUndoOf)
	}

	var written afterDoc
	if err := json.Unmarshal(snap.After, &written); err != nil {
		return action.Result{}, fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadSnapshot, err)
	}

	duration, err := time.ParseDuration(written.Duration)
	if err != nil {
		return action.Result{}, fmt.Errorf("%w: %w: %w", action.ErrPermanent, errBadSnapshot, err)
	}

	all, err := s.client.silences(ctx)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the silence: %w", err)
	}

	marked, running := 0, []silence{}

	for _, found := range all {
		if !mute.HasMarker(found.Comment, inv.UndoOf) {
			continue
		}

		marked++

		if found.State != stateExpired {
			running = append(running, found)
		}
	}

	switch {
	case marked == 0:
		return action.Result{Summary: "No silence of this proposal exists any more. It ended and was removed."}, nil
	case len(running) == 0:
		return action.Result{Summary: "The silence has ended already."}, nil
	}

	if !force {
		if edited := editedSilences(running, written, duration); len(edited) > 0 {
			live, err := liveJSON(edited)
			if err != nil {
				return action.Result{}, fmt.Errorf("write the live silence: %w", err)
			}

			return action.Result{}, &action.ConflictError{Live: live}
		}
	}

	for _, r := range running {
		if err := s.client.expire(ctx, r.ID); err != nil {
			return action.Result{}, fmt.Errorf("end the silence: %w", err)
		}
	}

	return s.result("Ended "+silenceCount(len(running))+" early.", running[0].ID), nil
}

// editedSilences returns the silences whose matchers or length differ from
// what was written.
func editedSilences(running []silence, written afterDoc, duration time.Duration) []silence {
	var edited []silence

	for _, r := range running {
		length := r.EndsAt.Sub(r.StartsAt)

		if !mute.SameMatchers(r.Matchers, written.Matchers) || length < duration-endTolerance || length > duration+endTolerance {
			edited = append(edited, r)
		}
	}

	return edited
}

// liveSilence is a silence as an approver reads it when Undo finds it
// edited.
type liveSilence struct {
	ID        string         `json:"id"`
	State     string         `json:"state"`
	Matchers  []mute.Matcher `json:"matchers"`
	StartsAt  time.Time      `json:"startsAt"`
	EndsAt    time.Time      `json:"endsAt"`
	CreatedBy string         `json:"createdBy"`
	Comment   string         `json:"comment"`
}

// liveJSON writes the silences as JSON: an object for one, a list for
// several.
func liveJSON(silences []silence) (json.RawMessage, error) {
	live := make([]liveSilence, 0, len(silences))

	for _, s := range silences {
		live = append(live, liveSilence{
			ID: s.ID, State: s.State, Matchers: s.Matchers, StartsAt: s.StartsAt, EndsAt: s.EndsAt, CreatedBy: s.CreatedBy, Comment: s.Comment,
		})
	}

	var v any = live
	if len(live) == 1 {
		v = live[0]
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return raw, nil
}

// silenceCount writes a number of silences, such as "1 silence".
func silenceCount(n int) string {
	if n == 1 {
		return "1 silence"
	}

	return strconv.Itoa(n) + " silences"
}

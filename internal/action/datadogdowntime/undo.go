package datadogdowntime

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

// lengthTolerance is how far the length of a live downtime may differ from
// the duration that was written before Undo calls it edited. Datadog sets
// the start when it makes the downtime, a moment after Sidekick chose the
// end.
const lengthTolerance = time.Minute

// Undo implements action.Undoer: it cancels the downtime early. The
// downtime is found by the marker of the proposal that made it. One that was
// canceled or has ended already, or that is gone, counts as success. When a
// downtime that still mutes no longer has the scope, the monitor or the
// length that were written, or has a recurring schedule, someone edited it,
// and unless force is set Undo cancels nothing and returns an
// *action.ConflictError with the live downtime. Canceling a downtime is the
// safe direction, so the limits are not checked again.
func (d *downtimer) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
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

	all, err := d.client.downtimes(ctx)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the downtime: %w", err)
	}

	marked, running := 0, []downtime{}

	for _, found := range all {
		if !hasMarker(found.Message, inv.UndoOf) {
			continue
		}

		marked++

		if found.Status == statusActive || found.Status == statusScheduled {
			running = append(running, found)
		}
	}

	switch {
	case marked == 0:
		return action.Result{Summary: "No downtime of this proposal exists any more. It was canceled and removed."}, nil
	case len(running) == 0:
		return action.Result{Summary: "The downtime has ended or was canceled already."}, nil
	}

	if !force {
		if edited := editedDowntimes(running, written, duration); len(edited) > 0 {
			live, err := liveJSON(edited)
			if err != nil {
				return action.Result{}, fmt.Errorf("write the live downtime: %w", err)
			}

			return action.Result{}, &action.ConflictError{Live: live}
		}
	}

	for _, r := range running {
		if err := d.client.cancel(ctx, r.ID); err != nil {
			return action.Result{}, fmt.Errorf("cancel the downtime: %w", err)
		}
	}

	return d.result("Canceled "+downtimeCount(len(running))+" early.", running[0].ID), nil
}

// editedDowntimes returns the downtimes whose scope, monitor or length
// differ from what was written, or whose schedule is no longer one-time.
func editedDowntimes(running []downtime, written afterDoc, duration time.Duration) []downtime {
	var edited []downtime

	for _, r := range running {
		if r.Scope != written.Scope || !sameMonitor(r, written) || !sameLength(r, duration) {
			edited = append(edited, r)
		}
	}

	return edited
}

// sameMonitor reports whether the downtime is for the monitor that was
// written: the same ID, or exactly the one tag.
func sameMonitor(r downtime, written afterDoc) bool {
	if written.MonitorTag != "" {
		return r.MonitorID == 0 && slices.Equal(r.MonitorTags, []string{written.MonitorTag})
	}

	return r.MonitorID == written.MonitorID && len(r.MonitorTags) == 0
}

// sameLength reports whether the downtime is a one-time one that lasts the
// duration that was written, give or take lengthTolerance.
func sameLength(r downtime, duration time.Duration) bool {
	if r.Recurring || r.Start.IsZero() || r.End.IsZero() {
		return false
	}

	length := r.End.Sub(r.Start)

	return length >= duration-lengthTolerance && length <= duration+lengthTolerance
}

// liveDowntime is a downtime as an approver reads it when Undo finds it
// edited.
type liveDowntime struct {
	ID          string    `json:"id"`
	Status      string    `json:"status"`
	Scope       string    `json:"scope"`
	Message     string    `json:"message"`
	MonitorID   int64     `json:"monitorId,omitempty"`
	MonitorTags []string  `json:"monitorTags,omitempty"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Recurring   bool      `json:"recurring,omitempty"`
}

// liveJSON writes the downtimes as JSON: an object for one, a list for
// several.
func liveJSON(downtimes []downtime) (json.RawMessage, error) {
	live := make([]liveDowntime, 0, len(downtimes))

	for _, r := range downtimes {
		live = append(live, liveDowntime(r))
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

// downtimeCount writes a number of downtimes, such as "1 downtime".
func downtimeCount(n int) string {
	if n == 1 {
		return "1 downtime"
	}

	return strconv.Itoa(n) + " downtimes"
}

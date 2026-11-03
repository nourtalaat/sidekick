package datadogdowntime

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// madeDowntime runs the proposal for the listed monitor against a new fake
// and returns the pieces an Undo needs.
func madeDowntime(t *testing.T) (*downtimer, *fakeDD, json.RawMessage, action.Snapshot) {
	t.Helper()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := monitorParams(t)

	snap, res := snapshotAndRun(t, d, raw)
	require.Equal(t, "Muted monitor 123 for 1h (downtime dt-1).", res.Summary)

	return d, dd, raw, snap
}

func TestUndoCancelsTheDowntime(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	res, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)

	require.Equal(t, []string{"dt-1"}, dd.canceled())
	require.Equal(t, "canceled", dd.get("dt-1").Status, "Datadog keeps a canceled downtime in its list")
	require.Equal(t, "Canceled 1 downtime early.", res.Summary)

	again, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "Undo is safe to repeat")
	require.Equal(t, "The downtime has ended or was canceled already.", again.Summary)
	require.Equal(t, []string{"dt-1"}, dd.canceled(), "a canceled downtime is not canceled again")
}

func TestUndoCancelsTheDowntimeOfATag(t *testing.T) {
	t.Parallel()

	api, dd := newFakeDD(t)
	d := newTestDowntimer(t, api, dd, testConfig(api.URL()))
	raw := tagParams(t)

	snap, _ := snapshotAndRun(t, d, raw)

	_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{"dt-1"}, dd.canceled())
}

func TestUndoOfADowntimeThatEndedAlreadyIsASuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeDD)
		want  string
	}{
		{name: "it ran out", setup: func(dd *fakeDD) { dd.edit("dt-1", func(f *fakeDowntime) { f.Status = "ended" }) }, want: "The downtime has ended or was canceled already."},
		{name: "a person canceled it", setup: func(dd *fakeDD) { dd.edit("dt-1", func(f *fakeDowntime) { f.Status = "canceled" }) }, want: "The downtime has ended or was canceled already."},
		{name: "it was removed", setup: func(dd *fakeDD) { dd.clear() }, want: "No downtime of this proposal exists any more."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, dd, raw, snap := madeDowntime(t)
			tt.setup(dd)

			res, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.want)
			require.Empty(t, dd.canceled())
		})
	}
}

func TestUndoOfAnEditedDowntimeIsAConflictUnlessForced(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*fakeDowntime)
	}{
		{name: "the scope changed", edit: func(f *fakeDowntime) { f.Scope = "*" }},
		{name: "another monitor", edit: func(f *fakeDowntime) { f.MonitorID = 999 }},
		{name: "every monitor with a tag", edit: func(f *fakeDowntime) { f.MonitorID, f.MonitorTags = 0, []string{"*"} }},
		{name: "extended", edit: func(f *fakeDowntime) { f.End = f.End.Add(5 * time.Hour) }},
		{name: "never ends", edit: func(f *fakeDowntime) { f.End = time.Time{} }},
		{name: "made recurring", edit: func(f *fakeDowntime) { f.Recurring = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, dd, raw, snap := madeDowntime(t)
			dd.edit("dt-1", tt.edit)

			_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
			require.ErrorIs(t, err, action.ErrConflict)

			var conflict *action.ConflictError

			require.ErrorAs(t, err, &conflict)
			require.Contains(t, string(conflict.Live), `"id":"dt-1"`, "the approver is shown the live downtime")
			require.Empty(t, dd.canceled(), "nothing is canceled without force")

			res, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, true)
			require.NoError(t, err, "force cancels it")
			require.Equal(t, []string{"dt-1"}, dd.canceled())
			require.Equal(t, "Canceled 1 downtime early.", res.Summary)
		})
	}
}

func TestUndoAcceptsALengthWithinAMinute(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	dd.edit("dt-1", func(f *fakeDowntime) { f.End = f.End.Add(30 * time.Second) })

	_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "Datadog sets the start a moment after Sidekick chose the end")
	require.Equal(t, []string{"dt-1"}, dd.canceled())
}

func TestUndoOfADowntimeWhoseMessageLostItsMarker(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	dd.edit("dt-1", func(f *fakeDowntime) { f.Message = "Planned database migration, now running" })

	res, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "No downtime of this proposal exists any more")
	require.Empty(t, dd.canceled(), "a downtime without the marker cannot be found, so it is left running")
}

func TestUndoFindsTheDowntimeWhenTheLineBreakBecameASpace(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	dd.edit("dt-1", func(f *fakeDowntime) {
		f.Message = "Planned database migration " + "[sidekick proposal " + proposalID + "]"
	})

	_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{"dt-1"}, dd.canceled())
}

func TestUndoLeavesTheDowntimesOfOtherProposalsAlone(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	dd.add(fakeDowntime{MonitorID: listedMonitor, Scope: prodScope, Message: messageWithMarker("Other work", otherProposalID), Start: testNow, End: testNow.Add(time.Hour)})

	_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{"dt-1"}, dd.canceled())
}

func TestUndoOfADowntimeThatIsGoneWhenItIsCanceled(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)
	dd.failWith(0, 0, 404, 0)

	res, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "the list showed it, and the cancel found it gone")
	require.Equal(t, []string{"dt-1"}, dd.canceled())
	require.Equal(t, "Canceled 1 downtime early.", res.Summary)
}

func TestUndoFailures(t *testing.T) {
	t.Parallel()

	d, dd, raw, snap := madeDowntime(t)

	bad := snap
	bad.After = json.RawMessage(`[1]`)

	noLength := snap
	noLength.After = json.RawMessage(`{"duration":"soon"}`)

	tests := []struct {
		name    string
		inv     action.Invocation
		snap    action.Snapshot
		wantErr error
	}{
		{name: "no proposal to undo", inv: action.Invocation{Params: raw}, snap: snap, wantErr: errNoUndoOf},
		{name: "a snapshot that cannot be read", inv: undoInvocation(raw, bad), snap: bad, wantErr: errBadSnapshot},
		{name: "a duration that cannot be read", inv: undoInvocation(raw, noLength), snap: noLength, wantErr: errBadSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := d.Undo(t.Context(), tt.inv, tt.snap, false)
			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, dd.canceled())
		})
	}
}

func TestUndoErrorsFromDatadog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		list      int
		del       int
		permanent bool
	}{
		{name: "the list is refused", list: 403, permanent: true},
		{name: "the list fails", list: 500},
		{name: "the cancel is refused", del: 403, permanent: true},
		{name: "the cancel fails", del: 503},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, dd, raw, snap := madeDowntime(t)
			dd.failWith(tt.list, 0, tt.del, 0)

			_, err := d.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
			require.Error(t, err)
			require.Equal(t, tt.permanent, errors.Is(err, action.ErrPermanent), "%v", err)
		})
	}
}

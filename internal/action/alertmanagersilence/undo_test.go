package alertmanagersilence

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// madeSilence runs the checkout proposal against a new fake and returns the
// pieces an Undo needs.
func madeSilence(t *testing.T) (*silencer, *fakeAM, json.RawMessage, action.Snapshot) {
	t.Helper()

	api, am := newFakeAM(t, "")
	s := newTestSilencer(t, api, am, testConfig(api.URL()))
	raw := checkoutParams(t)

	snap, res := snapshotAndRun(t, s, raw)
	require.Equal(t, "Silenced for 1h (silence sil-1).", res.Summary)

	return s, am, raw, snap
}

func TestUndoEndsTheSilence(t *testing.T) {
	t.Parallel()

	s, am, raw, snap := madeSilence(t)

	res, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)

	require.Equal(t, []string{"sil-1"}, am.deleted())
	require.Equal(t, "expired", am.stateOf("sil-1"), "Alertmanager keeps an ended silence")
	require.Equal(t, "Ended 1 silence early.", res.Summary)
	require.Equal(t, "Silence sil-1", res.Links[0].Title)

	again, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "Undo is safe to repeat")
	require.Equal(t, "The silence has ended already.", again.Summary)
	require.Equal(t, []string{"sil-1"}, am.deleted(), "an ended silence is not ended again")
}

func TestUndoOfASilenceThatEndedAlreadyIsASuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeAM)
		want  string
	}{
		{name: "it ran out", setup: func(am *fakeAM) { am.advance(2 * time.Hour) }, want: "The silence has ended already."},
		{name: "a person ended it", setup: func(am *fakeAM) {
			am.edit("sil-1", func(s *fakeSilence) { s.EndsAt = testNow })
			am.advance(time.Minute)
		}, want: "The silence has ended already."},
		{name: "it was removed", setup: func(am *fakeAM) {
			am.mu.Lock()
			defer am.mu.Unlock()

			am.silences = nil
		}, want: "No silence of this proposal exists any more"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, am, raw, snap := madeSilence(t)
			tt.setup(am)

			res, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.want)
			require.Empty(t, am.deleted(), "there is nothing to end")
		})
	}
}

func TestUndoLeavesOtherSilencesAlone(t *testing.T) {
	t.Parallel()

	s, am, raw, snap := madeSilence(t)

	other := am.add(fakeSilence{
		Matchers: decodeAfter(t, snap).Matchers, StartsAt: testNow, EndsAt: testNow.Add(time.Hour), CreatedBy: "ana",
		Comment: commentWithMarker("Planned database migration", otherProposalID),
	})

	_, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{"sil-1"}, am.deleted())
	require.Equal(t, "active", am.stateOf(other.ID))
}

func TestUndoShowsAnEditedSilenceAndForceEndsIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*fakeSilence)
		want bool
	}{
		{name: "other matchers", edit: func(s *fakeSilence) { s.Matchers = []mute.Matcher{eq("alertname", "Everything")} }, want: true},
		{name: "a matcher added", edit: func(s *fakeSilence) { s.Matchers = append(s.Matchers, eq("extra", "x")) }, want: true},
		{name: "extended by an hour", edit: func(s *fakeSilence) { s.EndsAt = s.EndsAt.Add(time.Hour) }, want: true},
		{name: "shortened by half an hour", edit: func(s *fakeSilence) { s.EndsAt = s.EndsAt.Add(-30 * time.Minute) }, want: true},
		{name: "ends a minute and a second late", edit: func(s *fakeSilence) { s.EndsAt = s.EndsAt.Add(time.Minute + time.Second) }, want: true},
		{name: "ends a minute late", edit: func(s *fakeSilence) { s.EndsAt = s.EndsAt.Add(time.Minute) }},
		{name: "starts half a minute late, as when clocks differ", edit: func(s *fakeSilence) { s.StartsAt = s.StartsAt.Add(30 * time.Second) }},
		{name: "the matchers in another order", edit: func(s *fakeSilence) { s.Matchers = []mute.Matcher{s.Matchers[2], s.Matchers[0], s.Matchers[1]} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, am, raw, snap := madeSilence(t)
			am.edit("sil-1", tt.edit)

			res, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)

			if !tt.want {
				require.NoError(t, err)
				require.Equal(t, []string{"sil-1"}, am.deleted())
				require.Equal(t, "Ended 1 silence early.", res.Summary)

				return
			}

			conflict, ok := errors.AsType[*action.ConflictError](err)
			require.True(t, ok, "got %v", err)
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, am.deleted(), "nothing is ended until the approver confirms")

			var live liveSilence
			require.NoError(t, json.Unmarshal(conflict.Live, &live), "one silence is shown as an object")
			require.Equal(t, "sil-1", live.ID)
			require.Equal(t, "active", live.State)
			require.NotEmpty(t, live.Matchers)

			_, err = s.Undo(t.Context(), undoInvocation(raw, snap), snap, true)
			require.NoError(t, err)
			require.Equal(t, []string{"sil-1"}, am.deleted())
			require.Equal(t, "expired", am.stateOf("sil-1"))
		})
	}
}

func TestUndoFindsASilenceThatGotANewID(t *testing.T) {
	t.Parallel()

	s, am, raw, snap := madeSilence(t)

	// An edit of the matchers ends the old silence and makes a new one with
	// a new ID, and the comment, with its marker, is copied.
	old := am.get("sil-1")
	am.edit("sil-1", func(s *fakeSilence) { s.EndsAt = testNow })
	am.advance(time.Minute)

	replacement := am.add(fakeSilence{Matchers: old.Matchers, StartsAt: am.clock(), EndsAt: testNow.Add(time.Hour), CreatedBy: "ana", Comment: old.Comment})

	_, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, []string{replacement.ID}, am.deleted(), "the ended one is left, the running one is ended")
}

func TestUndoWithSeveralEditedSilencesShowsAllOfThem(t *testing.T) {
	t.Parallel()

	s, am, raw, snap := madeSilence(t)
	old := am.get("sil-1")
	am.edit("sil-1", func(s *fakeSilence) { s.Matchers = []mute.Matcher{eq("alertname", "Everything")} })
	am.add(fakeSilence{Matchers: []mute.Matcher{eq("alertname", "Everything")}, StartsAt: testNow, EndsAt: old.EndsAt, CreatedBy: "ana", Comment: old.Comment})

	_, err := s.Undo(t.Context(), undoInvocation(raw, snap), snap, false)

	conflict, ok := errors.AsType[*action.ConflictError](err)
	require.True(t, ok, "got %v", err)

	var live []liveSilence
	require.NoError(t, json.Unmarshal(conflict.Live, &live))
	require.Len(t, live, 2)

	_, err = s.Undo(t.Context(), undoInvocation(raw, snap), snap, true)
	require.NoError(t, err)
	require.Equal(t, []string{"sil-1", "sil-2"}, am.deleted())
}

func TestUndoDoesNotCheckTheLimitsAgain(t *testing.T) {
	t.Parallel()

	api, am := newFakeAM(t, "")
	raw := checkoutParams(t)

	snap, _ := snapshotAndRun(t, newTestSilencer(t, api, am, testConfig(api.URL())), raw)

	cfg := testConfig(api.URL())
	cfg.Limits.Scopes = []mute.Scope{{Labels: map[string][]string{"team": {"nobody"}}}}

	_, err := newTestSilencer(t, api, am, cfg).Undo(t.Context(), undoInvocation(raw, snap), snap, false)
	require.NoError(t, err, "ending a silence is the safe direction")
	require.Equal(t, []string{"sil-1"}, am.deleted())
}

func TestUndoErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		list, deleted int
		noUndoOf      bool
		badSnapshot   bool
		wantErr       string
		wantPermanent bool
		wantOK        bool
	}{
		{name: "the silence is gone when Undo ends it", deleted: http.StatusNotFound, wantOK: true},
		{name: "the delete fails", deleted: http.StatusInternalServerError, wantErr: "answered 500"},
		{name: "the token may not delete", deleted: http.StatusForbidden, wantErr: "answered 403", wantPermanent: true},
		{name: "the list fails", list: http.StatusBadGateway, wantErr: "answered 502"},
		{name: "the undo names no proposal", noUndoOf: true, wantErr: errNoUndoOf.Error(), wantPermanent: true},
		{name: "a snapshot that is not a silence", badSnapshot: true, wantErr: errBadSnapshot.Error(), wantPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, am, raw, snap := madeSilence(t)
			am.failWith(tt.list, 0, 0)
			am.failDeleteWith(tt.deleted)

			inv := undoInvocation(raw, snap)
			if tt.noUndoOf {
				inv.UndoOf = ""
			}

			if tt.badSnapshot {
				snap = action.Snapshot{After: json.RawMessage(`"x"`)}
			}

			_, err := s.Undo(t.Context(), inv, snap, false)

			if tt.wantOK {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
		})
	}
}

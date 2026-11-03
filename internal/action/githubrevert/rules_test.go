package githubrevert

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestCheckAge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		landed    time.Time
		wantErr   string
		wantLimit bool
	}{
		{name: "just inside", landed: now.Add(-72 * time.Hour)},
		{name: "just outside", landed: now.Add(-72*time.Hour - time.Minute), wantErr: "longer than limits.maxAge", wantLimit: true},
		{name: "just now", landed: now},
		{name: "in the future", landed: now.Add(time.Hour)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkAge("commit abc", tt.landed, now, 72*time.Hour)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestHasRevertTrailer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "a trailer line", text: "Revert \"x\"\n\nThis reverts commit abc.\n\nSidekick-Revert: 0192d3a4\n", want: true},
		{name: "a trailer line with spaces before it", text: "Why.\n  Sidekick-Revert: 0192d3a4", want: true},
		{name: "the words in a sentence", text: "We discussed the Sidekick-Revert: marker.", want: false},
		{name: "the older proposal line", text: "Sidekick proposal 0192d3a4", want: false},
		{name: "empty", text: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, hasRevertTrailer(tt.text))
		})
	}
}

func TestHoldsWholeChange(t *testing.T) {
	t.Parallel()

	blob := func(sha string) entry { return entry{mode: "100644", blob: sha} }

	tests := []struct {
		name        string
		prPaths     []string
		changed     []string
		atParent    map[string]entry
		atMergeBase map[string]entry
		wantErr     bool
	}{
		{
			name:        "a squash of several commits",
			prPaths:     []string{"a.txt", "b.txt"},
			changed:     []string{"a.txt", "b.txt"},
			atParent:    map[string]entry{"a.txt": blob("a1"), "b.txt": {}},
			atMergeBase: map[string]entry{"a.txt": blob("a1"), "b.txt": {}},
		},
		{
			name:        "a squash with a renamed file",
			prPaths:     []string{"new.txt", "old.txt", "old.txt"},
			changed:     []string{"new.txt", "old.txt"},
			atParent:    map[string]entry{"new.txt": {}, "old.txt": blob("o1")},
			atMergeBase: map[string]entry{"new.txt": {}, "old.txt": blob("o1")},
		},
		{
			name:        "a rebase whose commits touch different files",
			prPaths:     []string{"a.txt", "b.txt"},
			changed:     []string{"b.txt"},
			atParent:    map[string]entry{"b.txt": blob("b1")},
			atMergeBase: map[string]entry{"b.txt": blob("b1")},
			wantErr:     true,
		},
		{
			name:        "a rebase whose commits touch the same file twice",
			prPaths:     []string{"a.txt"},
			changed:     []string{"a.txt"},
			atParent:    map[string]entry{"a.txt": blob("a2")},
			atMergeBase: map[string]entry{"a.txt": blob("a1")},
			wantErr:     true,
		},
		{
			name:        "a squash whose file changed on the base since",
			prPaths:     []string{"a.txt"},
			changed:     []string{"a.txt"},
			atParent:    map[string]entry{"a.txt": blob("a-base-moved")},
			atMergeBase: map[string]entry{"a.txt": blob("a1")},
			wantErr:     true,
		},
		{
			name:        "the merge commit changes a file the pull request does not list",
			prPaths:     []string{"a.txt"},
			changed:     []string{"a.txt", "c.txt"},
			atParent:    map[string]entry{"a.txt": blob("a1"), "c.txt": blob("c1")},
			atMergeBase: map[string]entry{"a.txt": blob("a1"), "c.txt": blob("c1")},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := holdsWholeChange(tt.prPaths, tt.changed, tt.atParent, tt.atMergeBase)
			if tt.wantErr {
				require.ErrorIs(t, err, errNotWholeChange)
				require.ErrorIs(t, err, action.ErrPermanent)

				return
			}

			require.NoError(t, err)
		})
	}
}

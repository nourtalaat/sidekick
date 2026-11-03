package githubrevert

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

// revertTrailer starts the line that marks a commit or a pull request
// description as made by this action. The line holds the proposal ID.
const revertTrailer = "Sidekick-Revert:"

// entry is a file in a tree: its mode, blob SHA and size. The zero value is
// a file that is not there.
type entry struct {
	mode string
	blob string
	size int
}

// exists reports whether the file is there.
func (e entry) exists() bool {
	return e.blob != ""
}

// refusal is an error for something the action will not do. The model reads
// it as a refusal, which names the reason, because it wraps action.ErrLimit,
// and Execute and Undo fail for good on it, because it wraps
// action.ErrPermanent. Without the first, Sidekick would tell the model to
// try again later, and it would propose the same revert again.
type refusal struct {
	err error
}

// Error implements error.
func (r *refusal) Error() string {
	return r.err.Error()
}

// Unwrap returns the reason, which may wrap a cause.
func (r *refusal) Unwrap() error {
	return r.err
}

// Is makes the refusal match action.ErrLimit and action.ErrPermanent.
func (*refusal) Is(target error) bool {
	return target == action.ErrLimit || target == action.ErrPermanent
}

// refuse returns a refusal with the message, which may use %w for a cause.
func refuse(format string, args ...any) error {
	return &refusal{err: fmt.Errorf(format, args...)}
}

// errNotWholeChange is returned for a pull request that may not be reverted
// by its merge commit alone.
var errNotWholeChange = refuse("the merge commit may not hold the whole change of the pull request, because it was merged by rebase or other commits changed the same files since it was opened; name the commits with commit instead, one revert for each")

// checkAge refuses what landed on the base branch longer ago than max. A
// time in the future counts as just now. what names the thing for the error.
func checkAge(what string, landed, now time.Time, max time.Duration) error {
	if age := now.Sub(landed); age > max {
		return fmt.Errorf("%s landed on the base branch %s ago, longer than limits.maxAge (%s): %w", what, age.Round(time.Minute), max, action.ErrLimit)
	}

	return nil
}

// hasRevertTrailer reports whether text, a commit message or a pull request
// description, has a line that marks it as a revert made by this action.
func hasRevertTrailer(text string) bool {
	for line := range strings.Lines(text) {
		if strings.HasPrefix(strings.TrimSpace(line), revertTrailer) {
			return true
		}
	}

	return false
}

// holdsWholeChange tells a squash merge from a rebase merge, for a pull
// request of more than one commit whose merge commit has one parent. GitHub
// gives both the same shape: for a squash, merge_commit_sha is the one
// commit that holds the whole change; for a rebase, it is only the last of
// several rebased commits, so reverting it leaves the others. Nothing in the
// pull request says which one it was, so the change is proven instead, from
// content alone, with two checks that must both hold:
//
//   - the files of the pull request (prPaths, with the old name of a renamed
//     file) are exactly the paths the merge commit changes (changed); and
//   - every changed path is the same in the merge commit's parent
//     (atParent) as at the point the pull request branched off (atMergeBase).
//
// A rebase fails one of them: an earlier commit touches a file the last one
// does not, or touches the same file first. A squash whose files other
// commits changed on the base branch since the pull request branched off
// fails the second check, and is refused, which is the safe direction: the
// error tells the model to name the commits instead.
func holdsWholeChange(prPaths, changed []string, atParent, atMergeBase map[string]entry) error {
	if !samePaths(prPaths, changed) {
		return errNotWholeChange
	}

	for _, path := range changed {
		if atParent[path] != atMergeBase[path] {
			return errNotWholeChange
		}
	}

	return nil
}

// uniqueSorted returns the paths in order, each once.
func uniqueSorted(paths []string) []string {
	return slices.Compact(slices.Sorted(slices.Values(paths)))
}

// samePaths reports whether two lists hold the same paths, whatever their
// order and repeats.
func samePaths(a, b []string) bool {
	return slices.Equal(uniqueSorted(a), uniqueSorted(b))
}

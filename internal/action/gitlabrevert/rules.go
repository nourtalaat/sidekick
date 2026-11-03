package gitlabrevert

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// revertTrailer starts the line that marks a merge request description as made
// by this action. The line holds the proposal ID.
const revertTrailer = "Sidekick-Revert:"

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

// checkAge refuses what landed on the base branch longer ago than limit. A
// time in the future counts as just now. what names the thing for the error.
func checkAge(what string, landed, now time.Time, limit time.Duration) error {
	if age := now.Sub(landed); age > limit {
		return fmt.Errorf("%s landed on the base branch %s ago, longer than limits.maxAge (%s): %w", what, age.Round(time.Minute), limit, action.ErrLimit)
	}

	return nil
}

// isSidekickRevert reports whether the merge request is a revert made by this
// action: it comes from a branch sidekick/<proposal id> and its description has
// the trailer line. GitLab's revert call takes no commit message, so the
// merge request is the only place the mark can be.
func isSidekickRevert(mr *gitlabgit.MergeRequest) bool {
	return gitlabgit.BranchOfProposal.MatchString(mr.SourceBranch) && hasRevertTrailer(mr.Description)
}

// hasRevertTrailer reports whether text has a line that marks it as a revert
// made by this action.
func hasRevertTrailer(text string) bool {
	for line := range strings.Lines(text) {
		if strings.HasPrefix(strings.TrimSpace(line), revertTrailer) {
			return true
		}
	}

	return false
}

// apiFailure turns a failed read into an error. A commit or merge request that
// GitLab does not know is the model's mistake and is refused, so it reads why;
// anything else is classified as a failure of the call.
func apiFailure(doing, what string, err error) error {
	if gitlabapi.IsStatus(err, http.StatusNotFound) {
		return refuse("%s was not found in the project", what)
	}

	return gitlabgit.Classify(doing, err)
}

// fileFailure maps what the shared reader cannot read as text to a refusal,
// naming the path, and wraps any other error.
func fileFailure(path string, err error) error {
	if isUnreadable(err) {
		return refuse("%q cannot be reverted by this action: %w; revert it by hand", path, err)
	}

	return fmt.Errorf("read %q: %w", path, err)
}

// isUnreadable reports whether err says a file or blob is not UTF-8 text, is
// too large, is not a regular file, or has a hash the reader does not support.
func isUnreadable(err error) bool {
	for _, sentinel := range []error{
		gitlabgit.ErrNotFile, gitlabgit.ErrTooBig, gitlabgit.ErrNotText,
		gitlabgit.ErrUnsupportedHash, gitlabgit.ErrNotBase64,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	return false
}

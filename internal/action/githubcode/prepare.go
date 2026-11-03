package githubcode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// loaded is a proposal read against the head of the base branch: the
// parameters that passed the limits, and what each changed file is there.
type loaded struct {
	params  Params
	owner   string
	name    string
	base    githubgit.Base
	changes []gitchange.PlannedFile
	// files is the state at the base of each changed path.
	files map[string]readFile
}

// load checks the parameters against the limits and reads the head of the
// base branch and each changed file at it.
func (c *Code) load(ctx context.Context, raw json.RawMessage) (*loaded, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return nil, fmt.Errorf("read the parameters: %w", err)
	}

	changes, err := c.limits.plan(params)
	if err != nil {
		return nil, fmt.Errorf("check the limits: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	head, err := githubgit.BaseOf(ctx, c.client, owner, name, params.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read the base branch: %w", err)
	}

	r := newReader(c.client, owner, name, head.Tree)
	files := make(map[string]readFile, len(changes))

	for _, ch := range changes {
		f, err := r.file(ctx, ch.Path)
		if err != nil {
			return nil, fmt.Errorf("read the file at the base: %w", err)
		}

		files[ch.Path] = f
	}

	return &loaded{params: params, owner: owner, name: name, base: head, changes: changes, files: files}, nil
}

// prepare loads the proposal, drops the files that the snapshot shows are at
// their new content already and refuses the ones that changed since it was
// taken, and counts the changed lines against the limit. snap is nil when
// there is no snapshot to compare with. Nothing is written. It runs when the
// model proposes and again right before the pull request is made, so a base
// branch that moved in between is checked too.
func (c *Code) prepare(ctx context.Context, raw json.RawMessage, snap *action.Snapshot) (*loaded, error) {
	l, err := c.load(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("read the proposal: %w", err)
	}

	if snap != nil {
		if err := l.applySnapshot(*snap); err != nil {
			return nil, fmt.Errorf("compare with the snapshot: %w", err)
		}
	}

	if len(l.changes) == 0 {
		return l, nil
	}

	if err := c.checkChanges(l); err != nil {
		return nil, fmt.Errorf("check the changes: %w", err)
	}

	return l, nil
}

// checkChanges counts the lines the changes add and remove together. A change
// that touches more than the limit, a file that is already as the model wants
// it, and a delete of a file that does not exist are refused.
func (c *Code) checkChanges(l *loaded) error {
	left := c.limits.MaxChangedLines

	for _, ch := range l.changes {
		f := l.files[ch.Path]

		if ch.Deleted() && !f.exists {
			return fmt.Errorf("%w: path %q does not exist on the base branch, so it cannot be deleted", action.ErrPermanent, ch.Path)
		}

		updated := ""
		if !ch.Deleted() {
			updated = *ch.Content
		}

		if f.exists && !ch.Deleted() && f.text == updated {
			return fmt.Errorf("%w: path %q already has this content; leave it out of the files", action.ErrPermanent, ch.Path)
		}

		n, within := gitchange.ChangedLines(f.text, updated, left)
		if !within {
			return fmt.Errorf("the changes add and remove more than the %d lines one pull request may change: %w", c.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= n
	}

	return nil
}

// applySnapshot compares the base branch now with the snapshot taken when the
// model proposed. A file at its before blob is written as planned. A file
// already at its after blob needs no change and is dropped from the plan. Any
// other file was changed by someone since, and the pull request would
// overwrite it, so the plan fails with action.ErrConflict naming the file.
func (l *loaded) applySnapshot(snap action.Snapshot) error {
	sides, err := gitchange.ReadSnapshot(snap)
	if err != nil {
		return fmt.Errorf("read the snapshot: %w", err)
	}

	keep := make([]gitchange.PlannedFile, 0, len(l.changes))

	for _, ch := range l.changes {
		after, ok := sides.After[ch.Path]
		if !ok {
			return fmt.Errorf("%w: the snapshot has no entry for %q", action.ErrPermanent, ch.Path)
		}

		if !ch.Deleted() && gitchange.BlobSHA(*ch.Content) != after.Blob {
			return fmt.Errorf("%w: the content of %q is not the content the snapshot describes", action.ErrPermanent, ch.Path)
		}

		live := l.files[ch.Path].sha

		switch {
		case live == gitchange.BlobOf(sides.Before[ch.Path]):
			keep = append(keep, ch)
		case live == after.Blob:
		default:
			return fmt.Errorf("%q changed on the base branch after the proposal was made: %w", ch.Path, action.ErrConflict)
		}
	}

	l.changes = keep

	return nil
}

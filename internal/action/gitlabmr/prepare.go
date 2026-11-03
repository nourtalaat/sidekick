package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// loaded is a proposal read against the head of the base branch: the
// parameters that passed the limits, and what each changed file is there.
type loaded struct {
	params  Params
	base    gitlabgit.Base
	changes []gitchange.PlannedFile
	reader  *gitlabgit.Reader
	// files is the state at the base of each changed path.
	files map[string]gitlabgit.ReadFile
}

// prepared is a merge request that passed every check, ready to write.
// changes are the files to write; it is empty when every file is at its new
// content on the base branch already.
type prepared struct {
	loaded
}

// load checks the parameters against the limits and reads the head of the
// base branch and each changed file at it.
func (m *MR) load(ctx context.Context, raw json.RawMessage) (*loaded, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return nil, fmt.Errorf("read the parameters: %w", err)
	}

	changes, err := m.limits.plan(params)
	if err != nil {
		return nil, fmt.Errorf("check the limits: %w", err)
	}

	head, err := gitlabgit.BaseOf(ctx, m.client, params.Project, params.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read the base branch: %w", err)
	}

	r := gitlabgit.NewReader(m.client, params.Project, head.Commit)
	files := make(map[string]gitlabgit.ReadFile, len(changes))

	for _, c := range changes {
		f, err := r.File(ctx, c.Path)
		if err != nil {
			return nil, readError("read the file at the base", err)
		}

		files[c.Path] = f
	}

	return &loaded{params: params, base: head, changes: changes, reader: r, files: files}, nil
}

// prepare loads the proposal, drops the files that the snapshot shows are at
// their new content already and refuses the ones that changed since it was
// taken, counts the changed lines against the limit and validates the
// files. snap is nil when there is no snapshot to compare with. Nothing is
// written. It runs when the model proposes and again right before the merge
// request is made, so a base branch that moved in between is checked too.
func (m *MR) prepare(ctx context.Context, raw json.RawMessage, snap *action.Snapshot) (*prepared, error) {
	l, err := m.load(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("read the proposal: %w", err)
	}

	if snap != nil {
		if err := l.applySnapshot(*snap); err != nil {
			return nil, fmt.Errorf("compare with the snapshot: %w", err)
		}
	}

	if len(l.changes) == 0 {
		return &prepared{loaded: *l}, nil
	}

	if err := m.checkLines(l); err != nil {
		return nil, fmt.Errorf("check the changed lines: %w", err)
	}

	if err := m.validateFiles(ctx, l.reader, l.changes); err != nil {
		return nil, fmt.Errorf("validate the files: %w", err)
	}

	return &prepared{loaded: *l}, nil
}

// checkLines counts the lines the changes add and remove together. A change
// that touches more than the limit, a file that is already as the model
// wants it, and a delete of a file that does not exist are refused.
func (m *MR) checkLines(l *loaded) error {
	left := m.limits.MaxChangedLines

	for _, c := range l.changes {
		f := l.files[c.Path]

		if c.Deleted() && !f.Exists {
			return gitchange.Refuse(fmt.Errorf("%w: path %q does not exist on the base branch, so it cannot be deleted", action.ErrPermanent, c.Path))
		}

		updated := ""
		if !c.Deleted() {
			updated = *c.Content
		}

		if f.Exists && !c.Deleted() && f.Text == updated {
			return gitchange.Refuse(fmt.Errorf("%w: path %q already has this content; leave it out of the files", action.ErrPermanent, c.Path))
		}

		n, within := gitchange.ChangedLines(f.Text, updated, left)
		if !within {
			return fmt.Errorf("the changes add and remove more than the %d lines one merge request may change: %w", m.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= n
	}

	return nil
}

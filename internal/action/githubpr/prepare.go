package githubpr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// loaded is a proposal read against the head of the base branch: the
// parameters that passed the limits, and what each changed file is there.
type loaded struct {
	params  Params
	owner   string
	name    string
	base    base
	changes []change
	reader  *reader
	// files is the state at the base of each changed path.
	files map[string]readFile
}

// prepared is a pull request that passed every check, ready to write.
// changes are the files to write; it is empty when every file is at its new
// content on the base branch already.
type prepared struct {
	loaded
}

// load checks the parameters against the limits and reads the head of the
// base branch and each changed file at it.
func (p *PR) load(ctx context.Context, raw json.RawMessage) (*loaded, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return nil, fmt.Errorf("read the parameters: %w", err)
	}

	changes, err := p.limits.plan(params)
	if err != nil {
		return nil, fmt.Errorf("check the limits: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	head, err := baseOf(ctx, p.client, owner, name, params.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read the base branch: %w", err)
	}

	r := newReader(p.client, owner, name, head.commit)
	files := make(map[string]readFile, len(changes))

	for _, c := range changes {
		f, err := r.file(ctx, c.path)
		if err != nil {
			return nil, fmt.Errorf("read the file at the base: %w", err)
		}

		files[c.path] = f
	}

	return &loaded{params: params, owner: owner, name: name, base: head, changes: changes, reader: r, files: files}, nil
}

// prepare loads the proposal, drops the files that the snapshot shows are at
// their new content already and refuses the ones that changed since it was
// taken, counts the changed lines against the limit and validates the
// files. snap is nil when there is no snapshot to compare with. Nothing is
// written. It runs when the model proposes and again right before the pull
// request is made, so a base branch that moved in between is checked too.
func (p *PR) prepare(ctx context.Context, raw json.RawMessage, snap *action.Snapshot) (*prepared, error) {
	l, err := p.load(ctx, raw)
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

	if err := p.checkLines(l); err != nil {
		return nil, fmt.Errorf("check the changed lines: %w", err)
	}

	if err := p.validateFiles(ctx, l.reader, l.changes); err != nil {
		return nil, fmt.Errorf("validate the files: %w", err)
	}

	return &prepared{loaded: *l}, nil
}

// checkLines counts the lines the changes add and remove together. A change
// that touches more than the limit, a file that is already as the model
// wants it, and a delete of a file that does not exist are refused.
func (p *PR) checkLines(l *loaded) error {
	left := p.limits.MaxChangedLines

	for _, c := range l.changes {
		f := l.files[c.path]

		if c.deleted() && !f.exists {
			return fmt.Errorf("%w: path %q does not exist on the base branch, so it cannot be deleted", action.ErrPermanent, c.path)
		}

		updated := ""
		if !c.deleted() {
			updated = *c.content
		}

		if f.exists && !c.deleted() && f.text == updated {
			return fmt.Errorf("%w: path %q already has this content; leave it out of the files", action.ErrPermanent, c.path)
		}

		n, within := changedLines(f.text, updated, left)
		if !within {
			return fmt.Errorf("the changes add and remove more than the %d lines one pull request may change: %w", p.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= n
	}

	return nil
}

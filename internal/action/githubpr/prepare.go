package githubpr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// prepared is a pull request that passed every check, ready to write.
type prepared struct {
	params  Params
	owner   string
	name    string
	base    base
	changes []change
}

// prepare checks the parameters against the limits, reads the files at the
// head of the base branch, counts the changed lines against the limit and
// validates the files. Nothing is written. It runs when the model proposes
// and again right before the pull request is made, so a base branch that
// moved in between is checked too.
func (p *PR) prepare(ctx context.Context, raw json.RawMessage) (*prepared, error) {
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

	if err := p.checkLines(ctx, r, changes); err != nil {
		return nil, fmt.Errorf("check the changed lines: %w", err)
	}

	if err := p.validateFiles(ctx, r, changes); err != nil {
		return nil, fmt.Errorf("validate the files: %w", err)
	}

	return &prepared{params: params, owner: owner, name: name, base: head, changes: changes}, nil
}

// checkLines reads each changed file at the base and counts the lines the
// changes add and remove together. A change that touches more than the
// limit, a file that is already as the model wants it, and a delete of a
// file that does not exist are refused.
func (p *PR) checkLines(ctx context.Context, r *reader, changes []change) error {
	left := p.limits.MaxChangedLines

	for _, c := range changes {
		old, exists, err := r.file(ctx, c.path)
		if err != nil {
			return fmt.Errorf("read the file at the base: %w", err)
		}

		if c.deleted() && !exists {
			return fmt.Errorf("%w: path %q does not exist on the base branch, so it cannot be deleted", action.ErrPermanent, c.path)
		}

		updated := ""
		if !c.deleted() {
			updated = *c.content
		}

		if exists && !c.deleted() && old == updated {
			return fmt.Errorf("%w: path %q already has this content; leave it out of the files", action.ErrPermanent, c.path)
		}

		n, within := changedLines(old, updated, left)
		if !within {
			return fmt.Errorf("the changes add and remove more than the %d lines one pull request may change: %w", p.limits.MaxChangedLines, action.ErrLimit)
		}

		left -= n
	}

	return nil
}

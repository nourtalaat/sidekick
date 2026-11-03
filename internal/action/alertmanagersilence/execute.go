package alertmanagersilence

import (
	"context"
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// Execute implements action.Action. It checks the parameters and the
// snapshot against the limits again, then looks for a silence that has the
// marker of the proposal, in any state. If there is one, an earlier attempt
// made it, and the work is done: a person may have ended it since, and it
// must not be made again. Otherwise it makes the silence from the snapshot,
// starting now and lasting the duration the approver read.
func (s *silencer) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	req, err := s.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	if inv.Snapshot == nil {
		return action.Result{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	doc, err := readAfter(inv.Snapshot, req)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	existing, err := s.client.silences(ctx)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the silence of an earlier attempt: %w", err)
	}

	for _, found := range existing {
		if mute.HasMarker(found.Comment, inv.ProposalID) {
			return s.result("Silence "+found.ID+" exists already.", found.ID), nil
		}
	}

	start := s.now().UTC()

	id, err := s.client.create(ctx, postedSilence{
		Matchers:  doc.Matchers,
		StartsAt:  start,
		EndsAt:    start.Add(req.duration),
		CreatedBy: doc.CreatedBy,
		Comment:   mute.WithMarker(doc.Comment, inv.ProposalID),
	})
	if err != nil {
		return action.Result{}, fmt.Errorf("make the silence: %w", err)
	}

	return s.result(fmt.Sprintf("Silenced for %s (silence %s).", mute.FormatDuration(req.duration), id), id), nil
}

// result is a result that links to the silence in the Alertmanager UI.
func (s *silencer) result(summary, id string) action.Result {
	return action.Result{
		Summary: summary,
		Links:   []action.Link{{Title: "Silence " + id, URL: s.client.root + "/#/silences/" + url.PathEscape(id)}},
	}
}

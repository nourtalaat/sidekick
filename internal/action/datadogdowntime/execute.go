package datadogdowntime

import (
	"context"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

// Execute implements action.Action. It checks the parameters and the
// snapshot against the limits again, then looks for a downtime that has the
// marker of the proposal, in any state the list shows. If there is one, an
// earlier attempt made it, and the work is done: a person may have canceled
// it since, and it must not be made again. Otherwise it checks that a
// monitor allowed only by its tags still has one, and makes the downtime
// from the snapshot, starting now and lasting the duration the approver
// read.
func (d *downtimer) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	req, err := d.parse(inv.Params)
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

	existing, err := d.client.downtimes(ctx)
	if err != nil {
		return action.Result{}, fmt.Errorf("look for the downtime of an earlier attempt: %w", err)
	}

	for _, found := range existing {
		if hasMarker(found.Message, inv.ProposalID) {
			return d.result("Downtime "+found.ID+" exists already.", found.ID), nil
		}
	}

	if req.monitorID != 0 && !slices.Contains(d.limits.MonitorIDs, req.monitorID) {
		if _, err := d.readMonitor(ctx, req.monitorID); err != nil {
			return action.Result{}, fmt.Errorf("check the monitor against the limits: %w", err)
		}
	}

	end := d.now().Add(req.duration)

	id, err := d.client.create(ctx, req, mute.WithMarker(doc.Message, inv.ProposalID), end)
	if err != nil {
		return action.Result{}, fmt.Errorf("make the downtime: %w", err)
	}

	return d.result(fmt.Sprintf("Muted %s for %s (downtime %s).", subject(req), mute.FormatDuration(req.duration), id), id), nil
}

// result is a result that links to the downtimes page of Datadog, when the
// link is known. Datadog has no address for one downtime.
func (d *downtimer) result(summary, id string) action.Result {
	res := action.Result{Summary: summary}

	if d.appHost != "" {
		res.Links = []action.Link{{Title: "Downtime " + id, URL: "https://" + d.appHost + "/monitors/downtimes"}}
	}

	return res
}

// subject names what the downtime mutes, for a summary.
func subject(req request) string {
	if req.monitorTag != "" {
		return "the monitors with tag " + req.monitorTag
	}

	return fmt.Sprintf("monitor %d", req.monitorID)
}

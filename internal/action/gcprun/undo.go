package gcprun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// Undo implements action.Undoer: it puts the traffic of the service back the
// way it was before the rollback, which is 100 percent to the latest ready
// revision, or to the revision that was named. When the service has that
// traffic already, nothing is written. When the traffic is not what the
// rollback wrote, someone changed it, and unless force is set Undo changes
// nothing and returns an *action.ConflictError with the revision that serves
// now.
func (r *Rollback) Undo(ctx context.Context, inv action.Invocation, snap action.Snapshot, force bool) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before undo: %w", err)
	}

	view, err := readSnapshot(&snap, req.service)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	svc, err := r.client.service(ctx, req.service)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the service: %w", classify(err))
	}

	restore := routing{kind: view.kind}
	if view.kind == trafficPinned {
		restore.revision = view.revisions.Running.ID
	}

	written := routing{kind: trafficPinned, revision: view.revisions.GoesBackTo.ID}

	live, liveErr := routingOf(svc)

	switch {
	case liveErr == nil && live == restore:
		return result(req.service, "Traffic of service "+req.service.name+" is back as it was before the rollback."), nil
	case !force && (liveErr != nil || live != written):
		doc, err := liveDocument(req.service, svc, live, liveErr)
		if err != nil {
			return action.Result{}, fmt.Errorf("write the live traffic: %w", err)
		}

		return action.Result{}, &action.ConflictError{Live: doc}
	}

	if err := r.apply(ctx, req.service, svc, restore); err != nil {
		return action.Result{}, fmt.Errorf("restore the traffic: %w", err)
	}

	summary := "Put the traffic of service " + req.service.name + " back: "
	if restore.kind == trafficLatest {
		summary += "all of it follows the latest ready revision again, so deploys get traffic."
	} else {
		summary += "all of it goes to revision " + restore.revision + "."
	}

	return result(req.service, summary), nil
}

// liveDocument writes the traffic of the service as it is now as a revision
// document, the form of Snapshot.Before, so an approver can see what a forced
// restore would overwrite. A traffic list that is not one entry is named by
// its entries.
func liveDocument(ref serviceRef, svc service, live routing, liveErr error) (json.RawMessage, error) {
	rev := action.Revision{Kind: revisionKind, ID: describeTraffic(svc.Traffic), Notes: "The traffic of the service is split, or has entries with a tag or no traffic."}

	if liveErr == nil {
		if name, err := live.runningName(svc); err == nil {
			rev.ID, rev.Notes = name, runningNotes(live, svc.LastModifier)
		}
	}

	snap, err := action.RevisionSnapshot(ref.target(), "", rev, rev)
	if err != nil {
		return nil, fmt.Errorf("make the revision document: %w", err)
	}

	return snap.Before, nil
}

// describeTraffic names the entries of a traffic list, such as
// "shop-00006 60 percent, shop-00005 40 percent".
func describeTraffic(traffic []trafficTarget) string {
	parts := make([]string, 0, len(traffic))

	for _, t := range traffic {
		parts = append(parts, fmt.Sprintf("%s %d percent", describeTarget(t), t.Percent))
	}

	if len(parts) == 0 {
		return "no revision"
	}

	return strings.Join(parts, ", ")
}

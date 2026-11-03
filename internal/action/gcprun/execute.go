package gcprun

import (
	"context"
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/action"
)

// snapshotView is what Execute and Undo read from the snapshot of a proposal:
// the two revisions, how the traffic reached the running one and the etag of
// the service when the proposal was made.
type snapshotView struct {
	revisions action.Revisions
	kind      string
	etag      string
}

// Execute implements action.Action. It checks the parameters and the snapshot
// again, then reads the service. When all the traffic goes to the revision of
// the snapshot already, an earlier attempt did the work, and nothing is
// written. When the etag, the way the traffic is set or the revision that
// serves differ from the snapshot, someone changed the service since the
// proposal, and the error wraps action.ErrConflict. Otherwise it sends the
// traffic to the revision the approver read, and waits for the operation.
func (r *Rollback) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	req, err := r.parse(inv.Params)
	if err != nil {
		return action.Result{}, fmt.Errorf("validate before execute: %w", err)
	}

	view, err := readSnapshot(inv.Snapshot, req.service)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the snapshot: %w", err)
	}

	svc, err := r.client.service(ctx, req.service)
	if err != nil {
		return action.Result{}, fmt.Errorf("read the service: %w", classify(err))
	}

	want := routing{kind: trafficPinned, revision: view.revisions.GoesBackTo.ID}

	if live, err := routingOf(svc); err == nil && live == want {
		return result(req.service, "Traffic of service "+req.service.name+" already goes to revision "+want.revision+"."), nil
	}

	if err := checkUnchanged(svc, view); err != nil {
		return action.Result{}, fmt.Errorf("check the service against the snapshot: %w", err)
	}

	if err := r.apply(ctx, req.service, svc, want); err != nil {
		return action.Result{}, fmt.Errorf("send the traffic to revision %s: %w", want.revision, err)
	}

	summary := "Sent all traffic of service " + req.service.name + " to revision " + want.revision + "."
	if view.kind == trafficLatest {
		summary += " The service is pinned to it: later deploys get no traffic until traffic is set back to the latest revision, which Undo does."
	} else {
		summary += " The service stays pinned to a named revision."
	}

	return result(req.service, summary), nil
}

// apply sets the traffic of the service to want, waits for the operation to
// finish, and checks the service has the traffic that was written.
func (r *Rollback) apply(ctx context.Context, ref serviceRef, svc service, want routing) error {
	op, err := r.client.setTraffic(ctx, ref, svc, want.targets())
	if err != nil {
		return fmt.Errorf("update the traffic: %w", classify(err))
	}

	for !op.Done {
		if op.Name == "" {
			return errNoOperation
		}

		if err := wait(ctx, r.pollInterval); err != nil {
			return fmt.Errorf("wait for the update to finish: %w", err)
		}

		if op, err = r.client.operation(ctx, op.Name); err != nil {
			return fmt.Errorf("wait for the update: %w", classify(err))
		}
	}

	if op.Error != nil {
		return operationError(op.Error.Code, op.Error.Message)
	}

	live, err := r.client.service(ctx, ref)
	if err != nil {
		return fmt.Errorf("read the service after the update: %w", classify(err))
	}

	if got, err := routingOf(live); err != nil || got != want {
		return errTrafficNotWritten
	}

	return nil
}

// checkUnchanged returns an error that wraps action.ErrConflict when the
// service is not as it was when the snapshot was made: another etag, traffic
// set another way, or another revision that serves.
func checkUnchanged(svc service, view snapshotView) error {
	rt, err := routingOf(svc)
	if err == nil {
		var name string

		if name, err = rt.runningName(svc); err == nil && svc.Etag == view.etag && rt.kind == view.kind && name == view.revisions.Running.ID {
			return nil
		}
	}

	return fmt.Errorf("%w: the service was changed after the proposal, by a deploy or an edit; propose the rollback again if it is still needed", action.ErrConflict)
}

// readSnapshot reads the snapshot of a rollback of the service. A snapshot
// that is missing, cannot be read or is of another service is a permanent
// failure.
func readSnapshot(snap *action.Snapshot, ref serviceRef) (snapshotView, error) {
	if snap == nil {
		return snapshotView{}, fmt.Errorf("%w: %w", action.ErrPermanent, errNoSnapshot)
	}

	revisions, err := snap.Revisions()
	if err != nil || revisions == nil {
		return snapshotView{}, fmt.Errorf("%w: %w", action.ErrPermanent, errBadSnapshot)
	}

	kind, etag, err := parseVersion(snap.Version)
	if err != nil {
		return snapshotView{}, fmt.Errorf("read the version of the snapshot: %w", err)
	}

	if snap.Target != ref.target() {
		return snapshotView{}, fmt.Errorf("%w: %w", action.ErrPermanent, errTargetDiffer)
	}

	return snapshotView{revisions: *revisions, kind: kind, etag: etag}, nil
}

// result is a result that links to the revisions of the service in the Google
// Cloud console.
func result(ref serviceRef, summary string) action.Result {
	link := "https://console.cloud.google.com/run/detail/" + url.PathEscape(ref.location) + "/" + url.PathEscape(ref.name) +
		"/revisions?project=" + url.QueryEscape(ref.project)

	return action.Result{Summary: summary, Links: []action.Link{{Title: "Cloud Run service " + ref.name, URL: link}}}
}

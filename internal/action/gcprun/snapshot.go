package gcprun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	// revisionKind is what Google Cloud calls a revision, for the Kind of an
	// action.Revision.
	revisionKind = "Cloud Run revision"
	// maxRevisionPages is how many pages of revisions the action reads.
	maxRevisionPages = 10
	// maxRevisions is how many revisions that is.
	maxRevisions = maxRevisionPages * revisionPageSize
	// maxSplitShown is how many entries of a split the refusal names.
	maxSplitShown = 5
	// fullTraffic is the percent of a traffic entry that has all the traffic.
	fullTraffic = 100
)

// The two ways the traffic of a service can be set, which Snapshot.Version
// names before the etag.
const (
	trafficLatest = "latest"
	trafficPinned = "pinned"
)

// routing is where the traffic of a service goes, when it all goes to one
// revision: to whichever revision is the latest ready one, or to one revision
// by name.
type routing struct {
	// kind is trafficLatest or trafficPinned.
	kind string
	// revision is the name of the revision, when kind is trafficPinned.
	revision string
}

// Snapshot implements action.Snapshotter. It reads the service and its
// revisions and refuses a rollback that cannot be made. Before is the revision
// that serves now and After the ready revision the rollback goes back to, and
// Version is the type of the traffic and the etag of the service. It changes
// nothing.
func (r *Rollback) Snapshot(ctx context.Context, raw json.RawMessage) (action.Snapshot, error) {
	req, err := r.parse(raw)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("validate the parameters: %w", err)
	}

	svc, err := r.client.service(ctx, req.service)
	if se, ok := errors.AsType[*statusError](err); ok && se.code == http.StatusNotFound {
		return action.Snapshot{}, refusal("service %q does not exist in project %q, location %q", req.service.name, req.service.project, req.service.location)
	}

	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the service: %w", err)
	}

	if svc.Reconciling {
		return action.Snapshot{}, refusal("the service is still applying an earlier change; propose again when it has finished")
	}

	rt, err := routingOf(svc)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the traffic of the service: %w", err)
	}

	runningName, err := rt.runningName(svc)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the revision that serves: %w", err)
	}

	list, more, err := r.client.revisions(ctx, req.service, maxRevisionPages)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("read the revisions: %w", err)
	}

	running, goesBackTo, err := pickRevisions(list, more, runningName, req.back)
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("find the revision to go back to: %w", err)
	}

	snap, err := action.RevisionSnapshot(req.service.target(), rt.version(svc.Etag),
		describe(running, runningNotes(rt, svc.LastModifier)), describe(goesBackTo, pinNotes(rt)))
	if err != nil {
		return action.Snapshot{}, fmt.Errorf("make the snapshot: %w", err)
	}

	return snap, nil
}

// runningName returns the name of the revision that serves the traffic.
func (rt routing) runningName(svc service) (string, error) {
	if rt.kind == trafficPinned {
		return rt.revision, nil
	}

	if svc.LatestReadyRevision == "" {
		return "", refusal("the service has no ready revision to roll back from")
	}

	return shortName(svc.LatestReadyRevision), nil
}

// targets returns the traffic list that sends all the traffic the way rt
// says.
func (rt routing) targets() []trafficTarget {
	if rt.kind == trafficLatest {
		return []trafficTarget{{Type: allocationLatest, Percent: fullTraffic}}
	}

	return []trafficTarget{{Type: allocationRevision, Revision: rt.revision, Percent: fullTraffic}}
}

// version is the Snapshot.Version of a rollback from a service with this
// routing and etag.
func (rt routing) version(etag string) string {
	return rt.kind + " " + etag
}

// routingOf says where the traffic of the service goes. It refuses a traffic
// list that is not one entry with all the traffic and no tag, because writing
// a list of one entry would drop the others.
func routingOf(svc service) (routing, error) {
	if len(svc.Traffic) == 0 {
		return routing{kind: trafficLatest}, nil
	}

	if len(svc.Traffic) == 1 {
		t := svc.Traffic[0]

		switch {
		case t.Percent != fullTraffic || t.Tag != "":
		case t.Type == allocationLatest:
			return routing{kind: trafficLatest}, nil
		case t.Type == allocationRevision && t.Revision != "":
			return routing{kind: trafficPinned, revision: shortName(t.Revision)}, nil
		}
	}

	var carrying []string

	for _, t := range svc.Traffic {
		if t.Percent > 0 {
			carrying = append(carrying, fmt.Sprintf("%s %d percent", describeTarget(t), t.Percent))
		}
	}

	if len(carrying) > 1 {
		if len(carrying) > maxSplitShown {
			carrying = append(carrying[:maxSplitShown], "more")
		}

		return routing{}, refusal("traffic is split between several revisions (%s); this action rolls back only a service that sends 100 percent of traffic to one revision",
			strings.Join(carrying, ", "))
	}

	return routing{}, refusal("the traffic list has more than one entry, or an entry with no traffic or a tag; setting the traffic would drop it, so this action does not change this service")
}

// parseVersion splits a Snapshot.Version into the type of the traffic and the
// etag.
func parseVersion(version string) (string, string, error) {
	kind, etag, ok := strings.Cut(version, " ")
	if !ok || etag == "" || (kind != trafficLatest && kind != trafficPinned) {
		return "", "", fmt.Errorf("%w: %w: the version is not the type of the traffic and an etag", action.ErrPermanent, errBadSnapshot)
	}

	return kind, etag, nil
}

// pickRevisions finds the revision that serves, named running, in the list
// of revisions of the service, newest first, and the back-th ready revision
// older than it. more says the list was cut short.
func pickRevisions(list []revision, more bool, running string, back int) (revision, revision, error) {
	at := -1

	for i, rev := range list {
		if shortName(rev.Name) == running {
			at = i

			break
		}
	}

	switch {
	case at < 0 && more:
		return revision{}, revision{}, refusal("the service has more than %d revisions; this action does not read that far", maxRevisions)
	case at < 0:
		return revision{}, revision{}, refusal("revision %q is not in the list of revisions of the service", running)
	}

	found := 0

	for _, rev := range list[at+1:] {
		if !rev.ready() {
			continue
		}

		if found++; found < back {
			continue
		}

		if err := checkOlder(list[at], rev); err != nil {
			return revision{}, revision{}, fmt.Errorf("check the order of the revisions: %w", err)
		}

		return list[at], rev, nil
	}

	switch {
	case more:
		return revision{}, revision{}, refusal("the service has more than %d revisions; this action does not read that far", maxRevisions)
	case found == 0:
		return revision{}, revision{}, refusal("the service has no earlier ready revision to go back to")
	default:
		return revision{}, revision{}, refusal("the service has only %d earlier ready revisions; %d back is not possible", found, back)
	}
}

// checkOlder refuses a target that is not older than the running revision,
// when both say when they were created.
func checkOlder(running, target revision) error {
	ranAt, targetAt := running.created(), target.created()
	if ranAt == nil || targetAt == nil || targetAt.Before(*ranAt) {
		return nil
	}

	return refusal("the revision to go back to is not older than the running one, so the list is not in creation order")
}

// ready reports whether the revision has its Ready condition succeeded.
func (rev revision) ready() bool {
	for _, c := range rev.Conditions {
		if c.Type == conditionReady && c.State == conditionSucceeded {
			return true
		}
	}

	return false
}

// created returns the creation time, or nil when it is missing or cannot be
// read.
func (rev revision) created() *time.Time {
	t, err := time.Parse(time.RFC3339Nano, rev.CreateTime)
	if err != nil {
		return nil
	}

	return &t
}

// describe makes the action.Revision of a revision, with notes.
func describe(rev revision, notes string) action.Revision {
	images := make([]string, 0, len(rev.Containers))

	for _, c := range rev.Containers {
		if c.Image != "" {
			images = append(images, c.Image)
		}
	}

	return action.Revision{
		Kind:       revisionKind,
		ID:         shortName(rev.Name),
		Version:    strings.Join(images, ", "),
		DeployedAt: rev.created(),
		DeployedBy: rev.Creator,
		Notes:      notes,
	}
}

// runningNotes is what a person reads about the revision that serves now: how
// the traffic reaches it, and who last changed the service, who may not be who
// made the revision.
func runningNotes(rt routing, lastModifier string) string {
	notes := "Traffic follows the latest ready revision."
	if rt.kind == trafficPinned {
		notes = "Traffic is pinned to this revision by name."
	}

	if lastModifier != "" {
		notes += " The service was last changed by " + lastModifier + ", who may not be the one who made this revision."
	}

	return notes
}

// pinNotes is what a person reads about the revision the rollback goes back
// to: that the service stays pinned to it.
func pinNotes(rt routing) string {
	if rt.kind == trafficPinned {
		return "The service stays pinned to a named revision, as it is now."
	}

	return "Pins the service to this revision: new deploys get no traffic until traffic is set back to the latest revision. Undo does that."
}

// describeTarget names the revision of a traffic entry.
func describeTarget(t trafficTarget) string {
	if t.Type == allocationLatest {
		return "the latest ready revision"
	}

	return shortName(t.Revision)
}

package azurecontainerapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v5"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The two ways traffic can be set, which the first word of Snapshot.Version
// says. "latest" is a weight that follows the latest revision; "pinned" is a
// weight on a revision by name. Undo puts back the way it was.
const (
	trafficLatest = "latest"
	trafficPinned = "pinned"
	// trafficUnknown is the mode of an app whose running revision cannot be
	// told, so it never matches a snapshot.
	trafficUnknown = "unknown"
)

// versionHashBytes is how many bytes of the SHA-256 hash the version keeps.
const versionHashBytes = 16

// fullWeight is the traffic weight of an app that sends all traffic to one
// revision.
const fullWeight = 100

// revisionKind is what Container Apps calls a revision, for action.Revision.
const revisionKind = "Container Apps revision"

// The notes of the two revisions of a rollback, for people to read.
const (
	notesFollowsLatest = "Traffic follows the latest revision."
	notesByName        = "Traffic goes to this revision by name."
	notesPinned        = "Pinned: the rollback sets traffic to this revision by name, so a new revision gets no traffic until someone moves it."
	notesMoves         = "Traffic moves to this revision by name; other settings, such as labels, stay."
)

// refusal is a state of the app that the action will not change. It wraps
// action.ErrLimit so that the model reads the message as a refusal, not as a
// failure to read the app.
type refusal string

// Error returns the message for the model.
func (r refusal) Error() string { return string(r) }

// Unwrap makes the refusal match action.ErrLimit.
func (refusal) Unwrap() error { return action.ErrLimit }

// refuse returns a refusal with the message made from format and args.
func refuse(format string, args ...any) error {
	return refusal(fmt.Sprintf(format, args...))
}

// live is a container app and its revisions as Azure reports them.
type live struct {
	app       armappcontainers.ContainerApp
	revisions []*armappcontainers.Revision
}

// running is the revision all the traffic of an app goes to.
type running struct {
	name string
	// latest is true when a weight follows the latest revision, so the
	// traffic is not on the revision by name.
	latest bool
}

// destination is where all the traffic should go.
type destination struct {
	revision string
	latest   bool
}

// fingerprint is what the version hashes: everything that decides where
// traffic goes.
type fingerprint struct {
	Mode        string         `json:"mode"`
	Latest      string         `json:"latest"`
	LatestReady string         `json:"latestReady"`
	Traffic     []trafficEntry `json:"traffic"`
}

// trafficEntry is one weight of the traffic list.
type trafficEntry struct {
	Revision string `json:"revision"`
	Weight   int32  `json:"weight"`
	Latest   bool   `json:"latest"`
	Label    string `json:"label"`
}

// mode returns the active revisions mode. Azure's default is Single.
func (l live) mode() armappcontainers.ActiveRevisionsMode {
	if p := l.app.Properties; p != nil && p.Configuration != nil && p.Configuration.ActiveRevisionsMode != nil {
		return *p.Configuration.ActiveRevisionsMode
	}

	return armappcontainers.ActiveRevisionsModeSingle
}

// ingress returns the ingress settings, or nil when the app has none.
func (l live) ingress() *armappcontainers.Ingress {
	if p := l.app.Properties; p != nil && p.Configuration != nil {
		return p.Configuration.Ingress
	}

	return nil
}

// traffic returns the traffic list, which may be empty.
func (l live) traffic() []*armappcontainers.TrafficWeight {
	if in := l.ingress(); in != nil {
		return in.Traffic
	}

	return nil
}

// latestName returns the name of the latest revision, and latestReadyName
// the name of the latest revision that is ready.
func (l live) latestName() string {
	if l.app.Properties == nil {
		return ""
	}

	return deref(l.app.Properties.LatestRevisionName)
}

func (l live) latestReadyName() string {
	if l.app.Properties == nil {
		return ""
	}

	return deref(l.app.Properties.LatestReadyRevisionName)
}

// inProgress reports whether Azure is still changing the app.
func (l live) inProgress() bool {
	p := l.app.Properties

	return p != nil && p.ProvisioningState != nil && *p.ProvisioningState == armappcontainers.ContainerAppProvisioningStateInProgress
}

// running tells which revision all the traffic goes to, or refuses when the
// app is in a state the action does not change. A weight that follows the
// latest revision is read as the latest revision that is ready, which Azure
// calls the latest stable revision; the list of revisions must agree, or the
// action refuses and does not guess.
func (l live) running(appName string) (running, error) {
	if l.mode() != armappcontainers.ActiveRevisionsModeMultiple {
		return running{}, refuse("container app %s is in single revision mode: Azure sends all of its traffic to the latest revision, "+
			"so there is no traffic to move. To go back, deploy the earlier template through your own pipeline; this action does not do that.", appName)
	}

	if l.ingress() == nil {
		return running{}, refuse("container app %s has no ingress, so it has no traffic weights to move.", appName)
	}

	weights, order, latest := l.weights()

	switch {
	case len(order) == 0:
		return running{}, refuse("container app %s sends no traffic to any revision, so there is no running revision to go back from.", appName)
	case len(order) > 1 || weights[order[0]] != fullWeight:
		return running{}, refuse("traffic of container app %s is split between several revisions (%s). "+
			"This action moves all of the traffic from one revision to another and does not change a split.", appName, splitText(weights, order))
	}

	run := running{name: order[0], latest: latest}

	if run.name == "" {
		return running{}, refuse("container app %s sends traffic to its latest revision, but Azure does not name a latest revision that is ready.", appName)
	}

	if l.revision(run.name) == nil {
		return running{}, refuse("container app %s sends its traffic to revision %s, which Azure does not list among its revisions.", appName, run.name)
	}

	if err := l.checkReported(appName, run.name); err != nil {
		return running{}, fmt.Errorf("check the reported traffic: %w", err)
	}

	return run, nil
}

// weights adds up the traffic list by revision: the weight of each revision
// that has any, in the order they first appear, and whether one of those
// weights follows the latest revision.
func (l live) weights() (weights map[string]int32, order []string, latest bool) {
	weights = map[string]int32{}

	for _, e := range l.traffic() {
		if e == nil || deref(e.Weight) <= 0 {
			continue
		}

		name := deref(e.RevisionName)

		if isLatest(e) {
			name, latest = l.latestReadyName(), true
		}

		if _, seen := weights[name]; !seen {
			order = append(order, name)
		}

		weights[name] += *e.Weight
	}

	return weights, order, latest
}

// checkReported checks that the list of revisions reports the traffic the
// ingress settings give: all of it on the running revision, none on another.
func (l live) checkReported(appName, name string) error {
	for _, r := range l.revisions {
		if r == nil || r.Properties == nil {
			continue
		}

		weight := deref(r.Properties.TrafficWeight)

		if (deref(r.Name) == name && weight != fullWeight) || (deref(r.Name) != name && weight > 0) {
			return refuse("Azure reports a different traffic split for container app %s than its ingress settings show. "+
				"Try again when a deployment has finished.", appName)
		}
	}

	return nil
}

// target picks the revision back revisions older than run, counting every
// revision by the time it was created, and refuses when there is not one or
// it is inactive.
func (l live) target(appName string, run running, back int) (*armappcontainers.Revision, error) {
	sorted := l.byAge()

	at := slices.IndexFunc(sorted, func(r *armappcontainers.Revision) bool { return deref(r.Name) == run.name })
	if at < 0 {
		return nil, refuse("container app %s sends its traffic to revision %s, which Azure does not list among its revisions.", appName, run.name)
	}

	if older := len(sorted) - 1 - at; back > older {
		return nil, refuse("container app %s has only %d revisions older than %s, so it cannot go %d back.", appName, older, run.name, back)
	}

	target := sorted[at+back]

	for _, r := range []*armappcontainers.Revision{sorted[at], target} {
		if created(r) == nil {
			return nil, refuse("revision %s has no created time, so its place in the order of revisions is not known.", deref(r.Name))
		}
	}

	if !isActive(target) {
		return nil, refuse("revision %s, %d back from %s, is inactive. Azure sends no traffic to an inactive revision, "+
			"and this action does not activate revisions. Activate it first, or ask for a different number of revisions back.",
			deref(target.Name), back, run.name)
	}

	return target, nil
}

// byAge returns the revisions with a name, the newest first. A revision
// without a created time is last, and equal times are ordered by name.
func (l live) byAge() []*armappcontainers.Revision {
	var out []*armappcontainers.Revision

	for _, r := range l.revisions {
		if r != nil && r.Name != nil {
			out = append(out, r)
		}
	}

	slices.SortFunc(out, func(a, b *armappcontainers.Revision) int {
		ta, tb := created(a), created(b)

		switch {
		case ta == nil && tb == nil:
			return strings.Compare(deref(a.Name), deref(b.Name))
		case ta == nil:
			return 1
		case tb == nil:
			return -1
		case !ta.Equal(*tb):
			return tb.Compare(*ta)
		default:
			return strings.Compare(deref(a.Name), deref(b.Name))
		}
	})

	return out
}

// revision returns the revision with the name, or nil.
func (l live) revision(name string) *armappcontainers.Revision {
	for _, r := range l.revisions {
		if r != nil && deref(r.Name) == name {
			return r
		}
	}

	return nil
}

// version is Snapshot.Version: the way traffic is set, then a hash of the
// mode, the two latest revision names and the whole traffic list. run is nil
// when the running revision could not be told.
func (l live) version(run *running) string {
	fp := fingerprint{Mode: string(l.mode()), Latest: l.latestName(), LatestReady: l.latestReadyName(), Traffic: []trafficEntry{}}

	for _, e := range l.traffic() {
		if e != nil {
			fp.Traffic = append(fp.Traffic, trafficEntry{Revision: deref(e.RevisionName), Weight: deref(e.Weight), Latest: isLatest(e), Label: deref(e.Label)})
		}
	}

	raw, err := json.Marshal(fp)
	if err != nil {
		raw = []byte(err.Error())
	}

	sum := sha256.Sum256(raw)
	mode := trafficUnknown

	if run != nil {
		mode = run.mode()
	}

	return mode + " " + hex.EncodeToString(sum[:versionHashBytes])
}

// trafficText lists the weights above 0, such as "checkout--a 60, latest 40".
func (l live) trafficText() string {
	var parts []string

	for _, e := range l.traffic() {
		if e == nil || deref(e.Weight) <= 0 {
			continue
		}

		name := deref(e.RevisionName)
		if isLatest(e) {
			name = "latest"
		}

		parts = append(parts, fmt.Sprintf("%s %d", name, *e.Weight))
	}

	return strings.Join(parts, ", ")
}

// mode says how the traffic of the running revision is set.
func (r running) mode() string {
	if r.latest {
		return trafficLatest
	}

	return trafficPinned
}

// modeOf returns the way traffic was set, from the first word of a
// snapshot's version.
func modeOf(version string) (string, bool) {
	mode, _, _ := strings.Cut(version, " ")

	return mode, mode == trafficLatest || mode == trafficPinned
}

// shift returns the traffic list with all the traffic on dest: every weight
// above 0 goes to 0, and the entry is dropped unless it has a label, and
// the entry of dest gets the full weight, or is added.
func shift(list []*armappcontainers.TrafficWeight, dest destination) []*armappcontainers.TrafficWeight {
	out := make([]*armappcontainers.TrafficWeight, 0, len(list)+1)
	placed := false

	for _, e := range list {
		if e == nil {
			continue
		}

		entry := *e

		switch {
		case !placed && forDestination(e, dest):
			entry.Weight, placed = new(int32(fullWeight)), true
		case deref(e.Weight) > 0 && deref(e.Label) == "":
			continue
		case deref(e.Weight) > 0:
			entry.Weight = new(int32(0))
		}

		out = append(out, &entry)
	}

	if !placed {
		added := &armappcontainers.TrafficWeight{Weight: new(int32(fullWeight))}

		if dest.latest {
			added.LatestRevision = new(true)
		} else {
			added.RevisionName = new(dest.revision)
		}

		out = append(out, added)
	}

	return out
}

// forDestination reports whether the entry is the one for dest.
func forDestination(e *armappcontainers.TrafficWeight, dest destination) bool {
	if dest.latest {
		return isLatest(e)
	}

	return !isLatest(e) && deref(e.RevisionName) == dest.revision
}

// describe returns the revision for a person to read.
func describe(r *armappcontainers.Revision, notes string) action.Revision {
	out := action.Revision{Kind: revisionKind, ID: deref(r.Name), Notes: notes, DeployedAt: created(r)}

	if r.Properties != nil && r.Properties.Template != nil {
		var images []string

		for _, c := range r.Properties.Template.Containers {
			if c != nil && c.Image != nil {
				images = append(images, *c.Image)
			}
		}

		out.Version = strings.Join(images, ", ")
	}

	return out
}

// splitText lists the weights of a split, such as "a 60, b 40".
func splitText(weights map[string]int32, order []string) string {
	parts := make([]string, 0, len(order))

	for _, name := range order {
		parts = append(parts, fmt.Sprintf("%s %d", name, weights[name]))
	}

	return strings.Join(parts, ", ")
}

// created returns when the revision was created, or nil.
func created(r *armappcontainers.Revision) *time.Time {
	if r == nil || r.Properties == nil {
		return nil
	}

	return r.Properties.CreatedTime
}

// isActive reports whether Azure says the revision is active.
func isActive(r *armappcontainers.Revision) bool {
	return r != nil && r.Properties != nil && r.Properties.Active != nil && *r.Properties.Active
}

// isLatest reports whether the weight follows the latest revision.
func isLatest(e *armappcontainers.TrafficWeight) bool {
	return e.LatestRevision != nil && *e.LatestRevision
}

// deref returns the value of p, or its zero value when p is nil.
func deref[T any](p *T) T {
	if p == nil {
		var zero T

		return zero
	}

	return *p
}

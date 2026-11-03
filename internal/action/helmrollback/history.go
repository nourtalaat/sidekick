package helmrollback

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/release"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	// revisionKind is what Helm calls a revision, as the proposal shows it.
	revisionKind = "Helm release revision"
	// maxDescriptionRunes is the most of the description Helm keeps with a
	// revision that goes into the notes of the proposal.
	maxDescriptionRunes = 200
	// maxListedRevisions is the most earlier revisions an error lists.
	maxListedRevisions = 10
	// storeSecret and storeConfigMap name the kind of object that holds the
	// records of a release, for the text of an error.
	storeSecret    = "Secret"
	storeConfigMap = "ConfigMap"
	// recordPrefix starts the name of each release record in the cluster
	// (pkg/storage/storage.go, HelmStorageType).
	recordPrefix = "sh.helm.release.v1"
)

// revision is what the action keeps of one revision of a Helm release.
type revision struct {
	version          int
	status           common.Status
	rollbackRevision int
	chartName        string
	chartVersion     string
	appVersion       string
	deployedAt       time.Time
	description      string
}

// readHistory turns what Helm's History returns into revisions, oldest
// first. It fails on a record that is not a release of Helm v1.
func readHistory(history []release.Releaser) ([]revision, error) {
	revs := make([]revision, 0, len(history))

	for _, h := range history {
		var rel *releasev1.Release

		switch r := h.(type) {
		case *releasev1.Release:
			rel = r
		case releasev1.Release:
			rel = &r
		default:
			return nil, fmt.Errorf("a record of the history is a %T, not a release", h)
		}

		if rel == nil {
			return nil, errors.New("a record of the history is empty")
		}

		revs = append(revs, toRevision(rel))
	}

	slices.SortFunc(revs, func(a, b revision) int { return a.version - b.version })

	return revs, nil
}

// toRevision reads the facts of one release record. A record with no info
// or no chart gives the facts it has, and the status unknown.
func toRevision(rel *releasev1.Release) revision {
	rev := revision{version: rel.Version, status: common.StatusUnknown}

	if rel.Info != nil {
		rev.status = rel.Info.Status
		rev.rollbackRevision = rel.Info.RollbackRevision
		rev.deployedAt = rel.Info.LastDeployed
		rev.description = rel.Info.Description
	}

	if rel.Chart != nil && rel.Chart.Metadata != nil {
		rev.chartName = rel.Chart.Metadata.Name
		rev.chartVersion = rel.Chart.Metadata.Version
		rev.appVersion = rel.Chart.Metadata.AppVersion
	}

	return rev
}

// choose finds the revision that runs and the one that goes back by back
// revisions. The revision that runs is the newest one, when its status is
// deployed or failed: a failed upgrade leaves the revision before it with
// the status deployed, but the objects in the cluster are the failed
// revision's. The revisions to go back to are the earlier ones with the
// status superseded or deployed. Every refusal wraps action.ErrLimit.
func choose(name, namespace string, revs []revision, back int) (running, target revision, err error) {
	running, err = runningOf(name, namespace, revs)
	if err != nil {
		return revision{}, revision{}, fmt.Errorf("find the revision that runs: %w", err)
	}

	candidates := earlier(revs, running.version)
	if len(candidates) < back {
		return revision{}, revision{}, refusal("%s", tooFew(name, running.version, candidates, back))
	}

	return running, candidates[back-1], nil
}

// runningOf returns the revision of a release that runs, or the refusal
// that says why Sidekick will not roll the release back.
func runningOf(name, namespace string, revs []revision) (revision, error) {
	if len(revs) == 0 {
		return revision{}, refusal("release %q was not found in namespace %q", name, namespace)
	}

	newest := revs[len(revs)-1]

	switch {
	case newest.status.IsPending():
		return revision{}, refusal("release %q has revision %d in status %s: another Helm operation is running on it or was interrupted, "+
			"so Sidekick will not roll it back; a person has to look at the release first", name, newest.version, newest.status)
	case !runs(newest):
		return revision{}, refusal("the newest revision %d of release %q has status %s; "+
			"Sidekick rolls back only a release whose newest revision is deployed or failed", newest.version, name, newest.status)
	default:
		return newest, nil
	}
}

// runs reports whether the objects of a revision are in the cluster: it was
// deployed, or it failed after Helm applied it.
func runs(r revision) bool {
	return r.status == common.StatusDeployed || r.status == common.StatusFailed
}

// canBeTarget reports whether a rollback may go back to the revision.
func canBeTarget(r revision) bool {
	return r.status == common.StatusSuperseded || r.status == common.StatusDeployed
}

// earlier returns the revisions before version that a rollback may go back
// to, the newest first.
func earlier(revs []revision, version int) []revision {
	var out []revision

	for i := len(revs) - 1; i >= 0; i-- {
		if revs[i].version < version && canBeTarget(revs[i]) {
			out = append(out, revs[i])
		}
	}

	return out
}

// tooFew is the message for a release with fewer revisions to go back to
// than were asked for.
func tooFew(name string, running int, candidates []revision, back int) string {
	if len(candidates) == 0 {
		return fmt.Sprintf("release %q is at revision %d and has no earlier revision with status superseded or deployed to roll back to", name, running)
	}

	var listed []string

	for i, c := range candidates {
		if i == maxListedRevisions {
			break
		}

		listed = append(listed, fmt.Sprintf("revision %d (%d back)", c.version, i+1))
	}

	return fmt.Sprintf("release %q is at revision %d; only %d earlier revision(s) can be rolled back to (status superseded or deployed): %s, so it cannot go back %d",
		name, running, len(candidates), strings.Join(listed, ", "), back)
}

// checkExecute compares the history, read right before a rollback, with the
// proposal: running is the number of the revision that ran at the proposal
// and target the one to go back to. It returns done when an earlier try made
// the rollback and it is in place, and an error when the rollback must not
// run: action.ErrConflict when the release changed, action.ErrPermanent when
// an interrupted or failed rollback needs a person.
func checkExecute(name, store string, revs []revision, running, target int) (done bool, err error) {
	if len(revs) == 0 {
		return false, fmt.Errorf("release %q is not there any more: %w", name, action.ErrConflict)
	}

	newest := revs[len(revs)-1]
	isRollback := newest.version == running+1 && newest.rollbackRevision == target

	switch {
	case isRollback && newest.status == common.StatusDeployed:
		return true, nil
	case isRollback && newest.status == common.StatusFailed:
		return false, fmt.Errorf("an earlier try rolled back release %q to revision %d as revision %d and it ended as failed: %s. "+
			"Sidekick does not repeat it; a person has to check the release: %w", name, target, newest.version, newest.description, action.ErrPermanent)
	case newest.status.IsPending():
		return false, interrupted(name, store, newest)
	case newest.version != running:
		return false, fmt.Errorf("release %q changed since the proposal: its newest revision is %d (status %s) and the proposal was made at revision %d: %w",
			name, newest.version, newest.status, running, action.ErrConflict)
	}

	if !slices.ContainsFunc(revs, func(r revision) bool { return r.version == target && canBeTarget(r) }) {
		return false, fmt.Errorf("revision %d of release %q is not in its history any more, or is not superseded or deployed "+
			"(Helm removes the oldest revisions when its history limit is reached): %w", target, name, action.ErrConflict)
	}

	return false, nil
}

// undoState says what an undo has to do.
type undoState int

const (
	// undoGo means the release is as the rollback left it.
	undoGo undoState = iota
	// undoDone means the release has been restored already.
	undoDone
	// undoConflict means the release is not as the rollback left it.
	undoConflict
)

// checkUndo compares the history with the rollback that is undone: it went
// from revision restore to revision target. It returns undoDone when the
// release has been put back already, undoConflict (with the revision that
// runs now) when someone changed it since the rollback, and an error when it
// cannot be restored at all.
func checkUndo(name, store string, revs []revision, restore, target int) (undoState, revision, error) {
	if len(revs) == 0 {
		return undoGo, revision{}, fmt.Errorf("release %q is not there any more: %w", name, action.ErrPermanent)
	}

	newest := revs[len(revs)-1]

	if newest.status.IsPending() {
		return undoGo, revision{}, interrupted(name, store, newest)
	}

	if !slices.ContainsFunc(revs, func(r revision) bool { return r.version == restore && canBeTarget(r) }) {
		return undoGo, revision{}, fmt.Errorf("revision %d of release %q is not in its history any more, or is not superseded or deployed "+
			"(Helm removes the oldest revisions when its history limit is reached): %w", restore, name, action.ErrPermanent)
	}

	switch {
	case newest.status == common.StatusDeployed && newest.rollbackRevision == restore && newest.version > restore:
		return undoDone, newest, nil
	case newest.status == common.StatusDeployed && newest.rollbackRevision == target && newest.version > restore:
		return undoGo, newest, nil
	default:
		return undoConflict, newest, nil
	}
}

// interrupted is the permanent failure for a release whose newest revision
// is pending: an operation that was interrupted leaves it, and Helm does not
// say whether it changed any object.
func interrupted(name, store string, newest revision) error {
	return fmt.Errorf("release %q has revision %d in status %s. An interrupted rollback leaves this status, and Helm does not say whether it changed any object. "+
		"Sidekick will not repeat it: a person has to check the release and the %s %s, and propose again if it is still needed: %w",
		name, newest.version, newest.status, store, recordName(name, newest.version), action.ErrPermanent)
}

// recordName is the name of the object that holds one revision of a release.
func recordName(name string, version int) string {
	return fmt.Sprintf("%s.%s.v%d", recordPrefix, name, version)
}

// storeKind says what kind of object the driver keeps the records in.
func storeKind(driver string) string {
	if driver == driverConfigMap {
		return storeConfigMap
	}

	return storeSecret
}

// document describes a revision for the approver. Helm does not record who
// deployed a revision, so DeployedBy stays empty and the notes say why.
func (r revision) document() action.Revision {
	doc := action.Revision{
		Kind:    revisionKind,
		ID:      strconv.Itoa(r.version),
		Version: r.versionText(),
		Notes:   r.notes(),
	}

	if !r.deployedAt.IsZero() {
		at := r.deployedAt.UTC()
		doc.DeployedAt = &at
	}

	return doc
}

// versionText is the chart and the app version, such as
// checkout-1.4.7 (app 2.9.1). It is empty when the chart is unknown.
func (r revision) versionText() string {
	if r.chartName == "" && r.chartVersion == "" {
		return ""
	}

	text := r.chartName + "-" + r.chartVersion

	if r.appVersion != "" {
		text += " (app " + r.appVersion + ")"
	}

	return text
}

// notes holds the status Helm gives the revision, the fact that Helm does
// not record who deployed it, and the description Helm kept.
func (r revision) notes() string {
	text := fmt.Sprintf("Helm status: %s. Helm does not record who deployed it.", r.status)

	if desc := shorten(r.description, maxDescriptionRunes); desc != "" {
		text += " Description: " + desc
	}

	return text
}

// shorten cuts s to at most limit characters, with "..." when it cut.
func shorten(s string, limit int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= limit {
		return string(runes)
	}

	return string(runes[:limit]) + "..."
}

// refusal is an error that says, for the model to read, what the action
// will not do, and wraps action.ErrLimit so that Sidekick ends the proposal
// at once.
func refusal(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), action.ErrLimit)
}

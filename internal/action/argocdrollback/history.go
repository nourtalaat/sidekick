package argocdrollback

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What a revision document of the action says.
const (
	// revisionKind is what Argo CD calls one entry of an application's
	// history: its own rollback error says "deployment with id".
	revisionKind = "Argo CD deployment"
	// automatedName is DeployedBy for a deployment that Argo CD's
	// auto-sync started.
	automatedName = "automated"
	// shortRevisionLen is how many characters of a commit a document shows.
	shortRevisionLen = 7
	// maxImages and maxNotesBytes bound the note of the running revision.
	maxImages     = 10
	maxNotesBytes = 512
)

// commitPattern is the shape of a full Git commit hash, SHA-1 or SHA-256.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// target is what a rollback goes from and to.
type target struct {
	// running is the newest history entry: what Argo CD last deployed.
	running argocd.HistoryEntry
	// goesBackTo is the entry that is back revisions before it.
	goesBackTo argocd.HistoryEntry
}

// plan finds the entry that runs and the entry that is back revisions
// before it. History is the successful syncs of the whole application, so
// its newest entry is what runs. Going back counts different revisions: an
// entry whose revision is one the walk has seen already, the running one
// included, is skipped, so going back 2 never lands on the revision that
// going back 1 does, or on the one that runs. A refusal wraps action.ErrLimit.
func plan(app *argocd.Application, ref argocd.AppRef, back int) (target, error) {
	entries := app.HistoryNewestFirst()
	if len(entries) == 0 {
		return target{}, refusal("Application %s has no deployment history, so there is nothing to go back to.", ref)
	}

	running := entries[0]
	seen := map[string]bool{running.RevisionText(): true}

	var earlier []argocd.HistoryEntry

	for _, e := range entries[1:] {
		if text := e.RevisionText(); !seen[text] {
			seen[text] = true

			earlier = append(earlier, e)
		}
	}

	if back > len(earlier) {
		return target{}, refusal("Application %s has %s in its history that differ%s from the one running now, so it cannot go back %d. "+
			"Argo CD keeps only the last %d deployments (revisionHistoryLimit), and older ones are gone.",
			ref, countText(len(earlier)), verbEnding(len(earlier)), back, app.HistoryLimit())
	}

	goesBackTo := earlier[back-1]
	if running.MultiSource() || goesBackTo.MultiSource() {
		return target{}, errSeveralSources(ref)
	}

	return target{running: running, goesBackTo: goesBackTo}, nil
}

// revisionOf describes a history entry as a revision document. notes is
// text for Notes, or empty.
func revisionOf(e argocd.HistoryEntry, notes string) action.Revision {
	r := action.Revision{
		Kind:       revisionKind,
		ID:         strconv.FormatInt(e.ID, 10),
		Version:    shortRevision(e.RevisionText()),
		DeployedBy: deployedBy(e.InitiatedBy),
		Notes:      notes,
	}

	if !e.DeployedAt.IsZero() {
		at := e.DeployedAt
		r.DeployedAt = &at
	}

	return r
}

// imagesNote lists the images the application runs now. Argo CD keeps no
// images for an older history entry, so only the running revision has this.
func imagesNote(app *argocd.Application) string {
	images := app.Status.Summary.Images
	if len(images) == 0 {
		return ""
	}

	shown := images[:min(len(images), maxImages)]
	text := "Images in the cluster now: " + strings.Join(shown, ", ")

	if more := len(images) - len(shown); more > 0 {
		text += fmt.Sprintf(" and %d more", more)
	}

	return source.CutText(text, maxNotesBytes)
}

// deployedBy says who started a deployment: the user, "automated", or empty
// when Argo CD did not record it.
func deployedBy(i argocd.Initiator) string {
	if i.Automated {
		return automatedName
	}

	return i.Username
}

// shortRevision shortens a full commit hash and leaves any other revision,
// such as a tag or a chart version, as it is.
func shortRevision(revision string) string {
	if commitPattern.MatchString(revision) {
		return revision[:shortRevisionLen]
	}

	return revision
}

// versionOf is the version of a snapshot: the ID and the revision of the
// newest history entry, which is what the conflict check compares.
func versionOf(e argocd.HistoryEntry) string {
	return strconv.FormatInt(e.ID, 10) + versionSeparator + e.RevisionText()
}

// splitVersion reads the ID and the revision from a snapshot's version.
func splitVersion(version string) (id int64, revision string, err error) {
	idText, revision, ok := strings.Cut(version, versionSeparator)
	if !ok {
		return 0, "", fmt.Errorf("the snapshot version %q is not an ID and a revision", version)
	}

	id, err = strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("the snapshot version %q has no history ID: %w", version, err)
	}

	return id, revision, nil
}

// countText says how many revisions there are: "1 earlier revision".
func countText(n int) string {
	if n == 1 {
		return "1 earlier revision"
	}

	return strconv.Itoa(n) + " earlier revisions"
}

// verbEnding is the ending of "differ" for n revisions.
func verbEnding(n int) string {
	if n == 1 {
		return "s"
	}

	return ""
}

package githubrevert

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The files of the standard repository, before and after the bad commit.
const (
	readmeText     = "readme\n"
	configBefore   = "retries: 3\ntimeout: 5s\n"
	configAfter    = "retries: 30\ntimeout: 5s\n"
	limitsText     = "max: 10\n"
	newFileText    = "package svc\n"
	configPath     = "svc/config.yaml"
	newFilePath    = "svc/new.go"
	limitsPath     = "svc/limits.yaml"
	readmePath     = "README.md"
	badCommitTitle = "Raise the retries"
)

// bed is a test action with the fake GitHub it talks to.
type bed struct {
	rev *Revert
	w   *world
}

// newBed returns an action that talks to a fresh fake GitHub, with the
// limits of validLimits changed by mutate. The fake GitHub is not serving
// yet: the test builds the repository, then calls b.w.serve().
func newBed(t *testing.T, mutate func(l *Limits)) *bed {
	t.Helper()

	return newBedWithDB(t, mutate, nil)
}

// newBedWithDB is newBed for an action that records pull request states in
// dbtx, which may be nil.
func newBedWithDB(t *testing.T, mutate func(l *Limits), dbtx db.DBTX) *bed {
	t.Helper()

	w := newWorld(t)

	limits := validLimits()
	limits.MaxFiles = 5

	if mutate != nil {
		mutate(&limits)
	}

	rev, err := newRevert("revert", Config{App: testApp, BaseURL: w.gh.URL(), Limits: limits}, registry.Deps{
		Secrets:   secretsWithKey(t),
		HTTP:      w.gh.Client(),
		PublicURL: "https://sidekick.example.com",
		DB:        dbtx,
	})
	require.NoError(t, err)

	rev.now = func() time.Time { return testNow }

	return &bed{rev: rev, w: w}
}

// history is the commits of the standard repository.
type history struct {
	// first is the commit before the bad one, bad the commit that raised the
	// retries, added a file and deleted the readme, and later one that only
	// changed another file.
	first, bad, later string
}

// standard builds the standard repository: a first commit, the bad commit on
// top of it, 1 hour old, and a later commit that changes only the limits
// file. main is at the later commit.
func (b *bed) standard() history {
	return b.standardAged(time.Hour)
}

// standardAged is standard with the bad commit made age ago.
func (b *bed) standardAged(age time.Duration) history {
	base := map[string]wfile{readmePath: txt(readmeText), configPath: txt(configBefore), limitsPath: txt(limitsText)}
	first := b.w.commit(nil, "Initial commit", testNow.Add(-100*time.Hour), base)

	broken := map[string]wfile{configPath: txt(configAfter), limitsPath: txt(limitsText), newFilePath: txt(newFileText)}
	bad := b.w.commit([]string{first}, badCommitTitle+"\n\nMore retries will help.", testNow.Add(-age), broken)

	later := map[string]wfile{configPath: txt(configAfter), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText)}
	head := b.w.commit([]string{bad}, "Raise the limit", testNow.Add(-30*time.Minute), later)

	b.w.setBranch("main", head)

	return history{first: first, bad: bad, later: head}
}

// byCommit returns parameters that revert a commit.
func byCommit(sha string) Params {
	return Params{Repo: testRepo, BaseBranch: "main", Commit: sha, Title: "Revert the retry change", Body: "It doubled the load on the database."}
}

// byPullRequest returns parameters that revert a pull request.
func byPullRequest(number int) Params {
	return Params{Repo: testRepo, BaseBranch: "main", PullRequest: number, Title: "Revert the retry change", Body: "It doubled the load on the database."}
}

// marshal encodes the parameters.
func marshal(t *testing.T, p Params) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	return raw
}

// snapshotOf takes the snapshot of the parameters, which must pass.
func (b *bed) snapshotOf(t *testing.T, p Params) action.Snapshot {
	t.Helper()

	snap, err := b.rev.Snapshot(t.Context(), marshal(t, p))
	require.NoError(t, err)

	return snap
}

// onMain makes the fake GitHub say that the commit is on main.
func (b *bed) onMain(sha string) {
	b.w.compare(sha, "main", "ahead", sha)
}

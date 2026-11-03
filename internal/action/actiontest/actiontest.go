// Package actiontest keeps the spec snapshots of the benchmark's mock
// actions in step with the real actions they come from.
package actiontest

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mock"
)

// snapshotDir is where the spec snapshots are, from the repository root.
const snapshotDir = "bench/actions"

// Permissions of the snapshot files and their directory.
const (
	fileMode = 0o600
	dirMode  = 0o750
)

// update rewrites the snapshot instead of checking it.
var update = flag.Bool("update-action-specs", false, "rewrite bench/actions/<type>.json from the action's Spec")

// CheckSpecSnapshot requires bench/actions/<typeName>.json to hold the
// Spec of a, so a benchmark scenario shows the model what a real
// deployment of the action shows it. Build a with the action's own test
// setup: it needs no network and no real secret, and its limits end up in
// the description, so use limits a real deployment could have. Run the
// test with -update-action-specs to write the file.
func CheckSpecSnapshot(t *testing.T, typeName string, a action.Action) {
	t.Helper()

	checkSnapshot(t, *update, filepath.Join(repoRoot(t), snapshotDir), typeName, a)
}

// checkSnapshot is CheckSpecSnapshot with the directory and the choice to
// write given.
func checkSnapshot(t *testing.T, write bool, dir, typeName string, a action.Action) {
	t.Helper()

	want, err := mock.SnapshotOf(a.Spec())
	require.NoError(t, err)

	path := filepath.Join(dir, typeName+".json")

	if write {
		require.NoError(t, os.MkdirAll(dir, dirMode))
		require.NoError(t, os.WriteFile(path, want, fileMode))
	}

	got, err := os.ReadFile(path) //nolint:gosec // The path is the repository's snapshot directory and a type name from a test.
	require.NoError(t, err, "run this test with -update-action-specs to write %s", path)
	require.Equal(t, string(want), string(got), "%s is not the Spec of the action: run this test with -update-action-specs", path)
}

// repoRoot returns the directory above the working directory that holds
// go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	require.NoError(t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above the working directory")

		dir = parent
	}
}

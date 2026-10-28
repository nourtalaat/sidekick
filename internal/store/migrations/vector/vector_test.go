package vector_test

import (
	"io/fs"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/migrations/vector"
)

var fileName = regexp.MustCompile(`^(\d{5})_[a-z0-9_]+\.sql$`)

// TestFileNames checks that the vector migrations are numbered 1, 2, 3 and
// so on, with no gaps or duplicates, apart from the main set.
func TestFileNames(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(vector.FS, ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for i, entry := range entries {
		match := fileName.FindStringSubmatch(entry.Name())
		require.NotNil(t, match, "%s is not named like 00001_short_name.sql", entry.Name())

		version, err := strconv.Atoi(match[1])
		require.NoError(t, err)
		require.Equal(t, i+1, version, "%s has the wrong version number", entry.Name())
	}
}

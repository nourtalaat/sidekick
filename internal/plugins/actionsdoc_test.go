package plugins_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	_ "github.com/nourtalaat/sidekick/internal/plugins"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const docsDir = "../../docs"

var (
	typeRow     = regexp.MustCompile("(?m)^\\| \\[`([a-z_.]+)`\\]\\(configuration\\.md#([^)]+)\\)")
	markdownRef = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	headingLine = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
)

func TestActionsPage(t *testing.T) {
	t.Parallel()

	page := readDoc(t, "actions.md")
	rows := typeRow.FindAllStringSubmatch(page, -1)

	t.Run("every registered type has exactly one row", func(t *testing.T) {
		t.Parallel()

		counts := map[string]int{}
		for _, row := range rows {
			counts[row[1]]++
		}

		for _, typ := range registry.Actions.Types() {
			require.Equal(t, 1, counts[typ], "action type %s must have exactly one row in docs/actions.md", typ)
			delete(counts, typ)
		}

		require.Empty(t, counts, "docs/actions.md has rows for types that are not registered")
	})

	t.Run("every row links to a section of the configuration reference", func(t *testing.T) {
		t.Parallel()

		anchors := anchorsOf(t, "configuration.md")
		for _, row := range rows {
			require.Contains(t, anchors, row[2], "the row of %s links to a missing section", row[1])
		}
	})

	t.Run("every link on the page resolves", func(t *testing.T) {
		t.Parallel()

		for _, ref := range markdownRef.FindAllStringSubmatch(page, -1) {
			target := ref[1]
			if strings.Contains(target, "://") {
				continue
			}

			file, anchor, _ := strings.Cut(target, "#")
			if file == "" {
				file = "actions.md"
			}

			path := filepath.Join(docsDir, file)
			_, err := os.Stat(path)
			require.NoError(t, err, "link %s", target)

			if anchor != "" {
				require.Contains(t, anchorsOf(t, file), anchor, "link %s", target)
			}
		}
	})
}

func readDoc(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(docsDir, name))
	require.NoError(t, err)

	return string(data)
}

// anchorsOf returns the anchors GitHub makes for the headings of a docs file,
// with the numeric suffix it adds to a repeated heading.
func anchorsOf(t *testing.T, name string) map[string]bool {
	t.Helper()

	anchors := map[string]bool{}
	seen := map[string]int{}
	inFence := false

	for _, line := range strings.Split(readDoc(t, name), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}

		if inFence {
			continue
		}

		m := headingLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		slug := slugOf(m[1])
		if n := seen[slug]; n > 0 {
			anchors[slug+"-"+strconv.Itoa(n)] = true
		} else {
			anchors[slug] = true
		}

		seen[slug]++
	}

	return anchors
}

func slugOf(heading string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}

	return b.String()
}

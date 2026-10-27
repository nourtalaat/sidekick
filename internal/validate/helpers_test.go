package validate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// toolsEnv names the directory `make validator-tools` installs into, with
// promtool, tofu and the provider mirror. CI sets it; without it, the tests
// that run the tools are skipped, except in CI, where they fail.
const toolsEnv = "SIDEKICK_VALIDATE_TOOLS"

// tools returns the options for a Service that uses the installed tools.
func tools(t *testing.T) Options {
	t.Helper()

	dir := os.Getenv(toolsEnv)
	if dir == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s is not set; CI must run make validator-tools and set it", toolsEnv)
		}

		t.Skipf("%s is not set; run make validator-tools and set it to bin/validator-tools", toolsEnv)
	}

	return Options{
		Promtool: filepath.Join(dir, "promtool"),
		Tofu:     filepath.Join(dir, "tofu"),
		Mirror:   filepath.Join(dir, "providers"),
		WorkDir:  t.TempDir(),
	}
}

// newService builds a Service and fails the test if it cannot.
func newService(t *testing.T, opts Options) *Service {
	t.Helper()

	if opts.WorkDir == "" {
		opts.WorkDir = t.TempDir()
	}

	s, err := New(opts)
	require.NoError(t, err)

	return s
}

// fixtureFiles reads every file under dir, dot files included, with paths
// relative to dir.
func fixtureFiles(t *testing.T, dir string) []File {
	t.Helper()

	var files []File

	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", name, err)
		}

		if d.IsDir() {
			return nil
		}

		content, err := os.ReadFile(name) //nolint:gosec // Test fixtures.
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return fmt.Errorf("relative path of %s: %w", name, err)
		}

		files = append(files, File{Path: filepath.ToSlash(rel), Content: string(content)})

		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, files, "no files under %s", dir)

	return files
}

// fixtureFile reads one fixture file as a request file named by its base
// name.
func fixtureFile(t *testing.T, name string) File {
	t.Helper()

	content, err := os.ReadFile(name) //nolint:gosec // Test fixtures.
	require.NoError(t, err)

	return File{Path: filepath.Base(name), Content: string(content)}
}

// messages returns the messages of problems, for readable failures.
func messages(problems []Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Path+": "+p.Message)
	}

	return out
}

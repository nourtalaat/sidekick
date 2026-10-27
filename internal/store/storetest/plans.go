//go:build integration

package storetest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// EnvPlansDir names the environment variable with a directory for query
// plans. When it is set, each plan an Explainer reads is also written, after
// its query, to <dir>/<package directory>/<test name>.txt, for example
// internal/queue/TestQueryPlans/ClaimJobs.txt. CI sets it and publishes the
// directory, so pull requests can show the plans of their queries.
const EnvPlansDir = "SIDEKICK_PLANS_DIR"

const (
	plansDirMode fs.FileMode = 0o750
	planFileMode fs.FileMode = 0o600
)

// writePlan writes sql and its plan to the file of the test named testName,
// under dir and the directory of the test's package.
func writePlan(dir, testName, sql, plan string) error {
	pkg, err := packageDir()
	if err != nil {
		return fmt.Errorf("find the package directory: %w", err)
	}

	return writePlanFile(filepath.Join(dir, pkg), testName, sql, plan)
}

// writePlanFile writes sql and its plan to <dir>/<testName>.txt. A subtest's
// name holds slashes, so each level of it becomes a directory.
func writePlanFile(dir, testName, sql, plan string) error {
	path := filepath.Join(dir, filepath.FromSlash(testName)+".txt")

	//nolint:gosec // The person running the tests chooses the plans directory.
	if err := os.MkdirAll(filepath.Dir(path), plansDirMode); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}

	content := strings.TrimSpace(sql) + "\n\n" + plan + "\n"

	//nolint:gosec // The person running the tests chooses the plans directory.
	if err := os.WriteFile(path, []byte(content), planFileMode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

// packageDir returns the directory of the test's package relative to the
// module root, for example internal/queue. Go runs each test binary in its
// package's directory.
func packageDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get the working directory: %w", err)
	}

	for root := wd; ; root = filepath.Dir(root) {
		_, err := os.Stat(filepath.Join(root, "go.mod"))
		if err == nil {
			rel, err := filepath.Rel(root, wd)
			if err != nil {
				return "", fmt.Errorf("find %s under %s: %w", wd, root, err)
			}

			return filepath.ToSlash(rel), nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("look for go.mod in %s: %w", root, err)
		}

		if filepath.Dir(root) == root {
			return "", fmt.Errorf("no go.mod above %s: %w", wd, fs.ErrNotExist)
		}
	}
}

package githubpr

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Limits on the Terraform files sent to the validator for one directory.
const (
	// maxModuleDepth is how many levels of local modules (source = "../x")
	// are followed from a changed module.
	maxModuleDepth = 3
	// maxModuleDirs is the most directories read for one changed module.
	maxModuleDirs = 20
	// maxModuleBytes is the most file text sent for one changed module.
	maxModuleBytes = 4 << 20
)

// terraformSuffixes are the files of a module that OpenTofu reads when it
// validates, and the lock file that pins its providers.
var terraformSuffixes = []string{".tf", ".tf.json", ".tofu", ".tofu.json", ".terraform.lock.hcl"}

// localModulePattern finds a module source that is a path in the same
// repository, such as "../modules/alert".
var localModulePattern = regexp.MustCompile(`(?m)^\s*source\s*=\s*"(\.{1,2}/[^"]*)"`)

// moduleLoad is the files read for one module so far.
type moduleLoad struct {
	files map[string]string
	dirs  []string
	bytes int
}

// terraformRequests makes one validation request for each module directory
// that a change touches: the files of the directory at the base commit, with
// the changes applied, and the local modules it uses.
func terraformRequests(ctx context.Context, r *reader, changes []gitchange.PlannedFile) ([]validateclient.Request, error) {
	var requests []validateclient.Request

	for _, dir := range terraformDirs(changes) {
		load := &moduleLoad{files: map[string]string{}}

		if err := load.dir(ctx, r, dir, 0); err != nil {
			return nil, fmt.Errorf("read the module in %s: %w", dir, err)
		}

		applyChanges(load.files, load.dirs, changes)

		requests = append(requests, validateclient.Request{Format: validateclient.FormatTerraform, Dir: dir, Files: sortedFiles(load.files)})
	}

	return requests, nil
}

// terraformDirs lists, in order, the directories of the module files that
// changes touch. Files of other kinds in the same directory, such as a
// README, do not make a module.
func terraformDirs(changes []gitchange.PlannedFile) []string {
	var dirs []string

	for _, c := range changes {
		if c.Format != validateclient.FormatTerraform || !isModuleFile(c.Path) {
			continue
		}

		if dir := path.Dir(c.Path); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}

	return dirs
}

// dir reads the module files of directory dir, and then the local modules
// that they use, down to maxModuleDepth.
func (m *moduleLoad) dir(ctx context.Context, r *reader, dir string, depth int) error {
	if slices.Contains(m.dirs, dir) {
		return nil
	}

	if len(m.dirs) >= maxModuleDirs {
		return fmt.Errorf("the module uses more than %d directories", maxModuleDirs)
	}

	m.dirs = append(m.dirs, dir)

	entries, err := r.dir(ctx, dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}

	var uses []string

	for _, e := range entries {
		if !isModuleFile(e.name) {
			continue
		}

		text, err := r.blob(ctx, e)
		if err != nil {
			return fmt.Errorf("read %s: %w", e.path, err)
		}

		if m.bytes += len(text); m.bytes > maxModuleBytes || len(m.files) >= validateclient.MaxFiles {
			return fmt.Errorf("the module has more than %d files or %d bytes", validateclient.MaxFiles, maxModuleBytes)
		}

		m.files[e.path] = text
		uses = append(uses, localModules(dir, text)...)
	}

	return m.follow(ctx, r, uses, depth)
}

// follow reads the local modules a directory uses.
func (m *moduleLoad) follow(ctx context.Context, r *reader, dirs []string, depth int) error {
	if depth >= maxModuleDepth {
		return nil
	}

	for _, d := range dirs {
		if err := m.dir(ctx, r, d, depth+1); err != nil {
			return fmt.Errorf("read the local module in %s: %w", d, err)
		}
	}

	return nil
}

// localModules returns the directories, from the root of the repository,
// of the local modules that the Terraform text in dir uses. A source that
// leaves the repository is left out; the validator then reports the module
// as missing.
func localModules(dir, text string) []string {
	var out []string

	for _, m := range localModulePattern.FindAllStringSubmatch(text, -1) {
		joined := path.Join(dir, m[1])
		if joined == ".." || strings.HasPrefix(joined, "../") {
			continue
		}

		out = append(out, joined)
	}

	return out
}

// applyChanges puts the changes into files, which are keyed by their path
// from the root of the repository: a file the model wrote replaces the
// one at the base, and a deleted file goes away. A change to a file that
// is not part of a module, or that is in a directory the module does not
// use, is left out.
func applyChanges(files map[string]string, dirs []string, changes []gitchange.PlannedFile) {
	for _, c := range changes {
		if c.Format != validateclient.FormatTerraform || !isModuleFile(c.Path) || !slices.Contains(dirs, path.Dir(c.Path)) {
			continue
		}

		if c.Deleted() {
			delete(files, c.Path)

			continue
		}

		files[c.Path] = *c.Content
	}
}

// isModuleFile reports whether the file name is one OpenTofu reads when it
// validates a module.
func isModuleFile(name string) bool {
	return slices.ContainsFunc(terraformSuffixes, func(suffix string) bool { return strings.HasSuffix(name, suffix) })
}

// sortedFiles returns files as validator files, ordered by path.
func sortedFiles(files map[string]string) []validateclient.File {
	out := make([]validateclient.File, 0, len(files))

	for p, content := range files {
		out = append(out, validateclient.File{Path: p, Content: content})
	}

	slices.SortFunc(out, func(a, b validateclient.File) int { return strings.Compare(a.Path, b.Path) })

	return out
}

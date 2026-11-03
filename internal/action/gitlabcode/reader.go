package gitlabcode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// Types and modes of the entries of a directory listing, as GitLab names them.
const (
	typeTree       = "tree"
	typeSubmodule  = "commit"
	modeFile       = "100644"
	modeExecutable = "100755"
)

var (
	errThroughFile = errors.New("goes through a path that is a file, a symlink or a submodule, not a directory")
	errCaseClash   = errors.New("differs only in case from a path that exists")
)

// readFileAt reads the file at path with the shared reader, after the
// directory listings have shown that it is a regular file, which the Files API
// does not say. A path that does not exist is returned with Exists false. A
// path that is a directory, a symlink or a submodule, a file that is too large
// and a file that is not text are refusals.
func readFileAt(ctx context.Context, r *gitlabgit.Reader, path string) (gitlabgit.ReadFile, error) {
	entry, err := entryOf(ctx, r, path)
	if err != nil {
		return gitlabgit.ReadFile{}, fmt.Errorf("find %s: %w", path, err)
	}

	if entry == nil {
		return gitlabgit.ReadFile{}, nil
	}

	if err := checkRegular(path, entry); err != nil {
		return gitlabgit.ReadFile{}, fmt.Errorf("check the entry: %w", err)
	}

	f, err := r.File(ctx, path)
	if err != nil {
		return gitlabgit.ReadFile{}, fmt.Errorf("read %s: %w", path, refusalOf(err))
	}

	return f, nil
}

// entryOf returns the listing entry at path, or nil when there is none. It
// walks the path one directory at a time. A directory on the way that is a
// file, a symlink or a submodule, a listing that GitLab cut short and a name
// that differs from an entry only in case are refusals.
func entryOf(ctx context.Context, r *gitlabgit.Reader, path string) (*gitlabgit.TreeEntry, error) {
	names := strings.Split(path, "/")
	dir := "."

	for i, name := range names {
		entries, err := r.Tree(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("list the directory above %q: %w", name, refusalOf(err))
		}

		entry, err := childOf(entries, name)
		if err != nil {
			return nil, refuse("look for %q: %w", name, err)
		}

		if entry == nil {
			return nil, nil
		}

		if i == len(names)-1 {
			return entry, nil
		}

		if entry.Type != typeTree {
			return nil, refuse("%q %w", name, errThroughFile)
		}

		dir = entry.Path
	}

	return nil, nil
}

// childOf returns the entry called name, or nil. A name that matches an entry
// only when case is ignored is an error: a file system that ignores case would
// treat the two as one file.
func childOf(entries []gitlabgit.TreeEntry, name string) (*gitlabgit.TreeEntry, error) {
	var clash *gitlabgit.TreeEntry

	for i := range entries {
		e := &entries[i]

		switch {
		case e.Name == name:
			return e, nil
		case strings.EqualFold(e.Name, name):
			clash = e
		}
	}

	if clash != nil {
		return nil, fmt.Errorf("%q %w (%q)", name, errCaseClash, clash.Name)
	}

	return nil, nil
}

// checkRegular returns a refusal unless the entry is a regular file, which is
// a blob with the mode 100644 or 100755. A symlink, a submodule and a
// directory are named in the error.
func checkRegular(path string, e *gitlabgit.TreeEntry) error {
	switch {
	case e.Type == typeTree:
		return refuse("path %q is a directory, not a file", path)
	case e.Mode == gitlabgit.ModeSymlink:
		return refuse("path %q is a symlink, which this action does not change", path)
	case e.Type == typeSubmodule:
		return refuse("path %q is a submodule, which this action does not change", path)
	case !isRegular(e):
		return refuse("path %q is not a regular file (mode %s)", path, e.Mode)
	}

	return nil
}

// isRegular reports whether the entry is a blob with the mode 100644 or
// 100755.
func isRegular(e *gitlabgit.TreeEntry) bool {
	return e.Type == gitlabgit.EntryBlob && (e.Mode == modeFile || e.Mode == modeExecutable)
}

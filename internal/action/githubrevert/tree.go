package githubrevert

import (
	"context"
	"fmt"
	"slices"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// File modes of a tree entry, and the type GitHub gives a directory.
const (
	modeFile       = "100644"
	modeExecutable = "100755"
	modeSymlink    = "120000"
	modeSubmodule  = "160000"
	typeTree       = "tree"
	typeBlob       = "blob"
)

// maxTreeReads is the most trees one revert may read. A change of at most
// maxFiles files in directories no deeper than this allows is far below it;
// it stops a commit that spreads over many directories from costing many
// calls.
const maxTreeReads = 500

// change is one file the commit changed: what the first parent had and what
// the commit left. The zero entry is a file that is not there.
type change struct {
	path   string
	parent entry
	commit entry
}

// trees reads the trees of one repository, each by its SHA once.
type trees struct {
	client *gh.Client
	owner  string
	repo   string
	cache  map[string][]*gh.TreeEntry
	reads  int
}

// newTrees returns a reader of the trees of owner/repo.
func newTrees(client *gh.Client, owner, repo string) *trees {
	return &trees{client: client, owner: owner, repo: repo, cache: map[string][]*gh.TreeEntry{}}
}

// diff returns the files that differ between two trees, by path. from is
// the tree of the parent and to the tree of the commit. It refuses more than
// limit files, and what a revert cannot write back: a file that became a
// directory or the reverse, a submodule, a symbolic link and a change of
// file mode.
func (t *trees) diff(ctx context.Context, from, to string, limit int) ([]change, error) {
	w := &walker{trees: t, limit: limit}

	if err := w.walk(ctx, from, to, ""); err != nil {
		return nil, fmt.Errorf("compare the trees: %w", err)
	}

	slices.SortFunc(w.found, func(a, b change) int { return strings.Compare(a.path, b.path) })

	return w.found, nil
}

// at returns the file at path in the tree with the SHA root. A path that
// is not there, or that has a file where a directory should be, gives the
// zero entry.
func (t *trees) at(ctx context.Context, root, path string) (entry, error) {
	sha := root
	segments := strings.Split(path, "/")

	for i, segment := range segments {
		entries, err := t.read(ctx, sha)
		if err != nil {
			return entry{}, fmt.Errorf("read the directory of %q: %w", path, err)
		}

		e := find(entries, segment)

		switch {
		case e == nil:
			return entry{}, nil
		case i == len(segments)-1:
			return entryOf(e), nil
		case e.GetType() != typeTree:
			return entry{}, nil
		}

		sha = e.GetSHA()
	}

	return entry{}, nil
}

// read returns the entries of the tree with the SHA, one directory level. An
// empty SHA is the empty tree.
func (t *trees) read(ctx context.Context, sha string) ([]*gh.TreeEntry, error) {
	if sha == "" {
		return nil, nil
	}

	if entries, ok := t.cache[sha]; ok {
		return entries, nil
	}

	if t.reads >= maxTreeReads {
		return nil, fmt.Errorf("the change touches more than %d directories: %w", maxTreeReads, action.ErrLimit)
	}

	t.reads++

	tree, _, err := t.client.Git.GetTree(ctx, t.owner, t.repo, sha, false)
	if err != nil {
		return nil, githubgit.Classify("read the tree "+sha, err)
	}

	if tree.GetTruncated() {
		return nil, fmt.Errorf("the tree %s has too many entries for GitHub to list: %w", sha, action.ErrLimit)
	}

	t.cache[sha] = tree.Entries

	return tree.Entries, nil
}

// walker collects the files that differ between two trees.
type walker struct {
	*trees
	limit int
	found []change
}

// walk compares two trees, which may be empty, and descends only into the
// directories whose SHA differs.
func (w *walker) walk(ctx context.Context, from, to, prefix string) error {
	old, err := w.read(ctx, from)
	if err != nil {
		return fmt.Errorf("read the tree of the parent: %w", err)
	}

	updated, err := w.read(ctx, to)
	if err != nil {
		return fmt.Errorf("read the tree of the commit: %w", err)
	}

	for _, name := range names(old, updated) {
		a, b := find(old, name), find(updated, name)

		if sameEntry(a, b) {
			continue
		}

		if err := w.step(ctx, prefix+name, a, b); err != nil {
			return fmt.Errorf("compare %q: %w", prefix+name, err)
		}
	}

	return nil
}

// step handles one name that differs: a directory is compared in turn, a
// file is a change.
func (w *walker) step(ctx context.Context, path string, a, b *gh.TreeEntry) error {
	switch {
	case isTree(a) && isTree(b):
		return w.walk(ctx, a.GetSHA(), b.GetSHA(), path+"/")
	case isTree(a) && b == nil:
		return w.walk(ctx, a.GetSHA(), "", path+"/")
	case isTree(b) && a == nil:
		return w.walk(ctx, "", b.GetSHA(), path+"/")
	case isTree(a) || isTree(b):
		return fmt.Errorf("%w: %q became a directory or stopped being one in this change; revert it by hand", action.ErrPermanent, path)
	}

	c := change{path: path, parent: entryOf(a), commit: entryOf(b)}

	if err := checkChange(c); err != nil {
		return fmt.Errorf("check %q: %w", path, err)
	}

	if len(w.found) >= w.limit {
		return fmt.Errorf("the change touches more than the %d files one revert may change: %w", w.limit, action.ErrLimit)
	}

	w.found = append(w.found, c)

	return nil
}

// checkChange refuses a change that a revert cannot write back: it is only
// about regular files, and their mode stays as it is, because the snapshot
// an approver reads shows blobs and not modes.
func checkChange(c change) error {
	for _, e := range []entry{c.parent, c.commit} {
		switch e.mode {
		case "", modeFile, modeExecutable:
		case modeSymlink:
			return fmt.Errorf("%w: %q is a symbolic link; revert it by hand", action.ErrPermanent, c.path)
		case modeSubmodule:
			return fmt.Errorf("%w: %q is a submodule; revert it by hand", action.ErrPermanent, c.path)
		default:
			return fmt.Errorf("%w: %q has the file mode %s, which a revert cannot write; revert it by hand", action.ErrPermanent, c.path, e.mode)
		}
	}

	if c.parent.exists() && c.commit.exists() && c.parent.mode != c.commit.mode {
		return fmt.Errorf("%w: the change gives %q another file mode; revert it by hand", action.ErrPermanent, c.path)
	}

	return nil
}

// entryOf returns the mode, blob and size of a tree entry; nil is the zero
// entry.
func entryOf(e *gh.TreeEntry) entry {
	if e == nil {
		return entry{}
	}

	return entry{mode: e.GetMode(), blob: e.GetSHA(), size: e.GetSize()}
}

// isTree reports whether the entry is a directory.
func isTree(e *gh.TreeEntry) bool {
	return e != nil && e.GetType() == typeTree
}

// sameEntry reports whether two entries, either of which may be nil, are
// the same file or directory.
func sameEntry(a, b *gh.TreeEntry) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.GetSHA() == b.GetSHA() && a.GetMode() == b.GetMode()
}

// find returns the entry with the name, or nil.
func find(entries []*gh.TreeEntry, name string) *gh.TreeEntry {
	for _, e := range entries {
		if e.GetPath() == name {
			return e
		}
	}

	return nil
}

// names returns the names in either list, sorted and once each.
func names(lists ...[]*gh.TreeEntry) []string {
	var all []string

	for _, entries := range lists {
		for _, e := range entries {
			all = append(all, e.GetPath())
		}
	}

	return slices.Compact(slices.Sorted(slices.Values(all)))
}

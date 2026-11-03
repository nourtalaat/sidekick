package githubcode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// Modes and types of tree entries, as the Git data API names them.
const (
	modeExecutable = "100755"
	modeSymlink    = "120000"
	modeSubmodule  = "160000"
	typeBlob       = "blob"
	typeTree       = "tree"
	typeCommit     = "commit"
)

var (
	errThroughFile = errors.New("goes through a path that is a file, a symlink or a submodule, not a directory")
	errCaseClash   = errors.New("differs only in case from a path that exists")
	errTreeTooBig  = errors.New("is in a directory that GitHub lists only in part")
)

// reader reads the files of one repository below one tree, with the Git
// trees API, because that is the call that tells the mode of each entry.
// The Contents API does not, and it answers for a symlink with the file the
// link points to. It remembers what it read, so a directory that several
// files share is listed once.
type reader struct {
	client *gh.Client
	owner  string
	repo   string
	// root is the SHA of the tree every path starts from.
	root string

	trees map[string]*gh.Tree
	files map[string]readFile
}

// readFile is the result of reading one path: its text, blob SHA and mode,
// or that it does not exist.
type readFile struct {
	text   string
	sha    string
	mode   string
	exists bool
}

// newReader returns a reader of the files below the tree with the SHA root.
func newReader(client *gh.Client, owner, repo, root string) *reader {
	return &reader{client: client, owner: owner, repo: repo, root: root, trees: map[string]*gh.Tree{}, files: map[string]readFile{}}
}

// file reads the file at path. A path that does not exist is returned with
// exists false. A path that is a directory, a symlink or a submodule, a file
// that is too large and a file that is not text are errors that wrap
// action.ErrPermanent, because the same path fails again.
func (r *reader) file(ctx context.Context, path string) (readFile, error) {
	if f, ok := r.files[path]; ok {
		return f, nil
	}

	entry, err := r.entry(ctx, path)
	if err != nil {
		return readFile{}, fmt.Errorf("find %s: %w", path, err)
	}

	if entry == nil {
		r.files[path] = readFile{}

		return readFile{}, nil
	}

	if err := checkRegular(path, entry); err != nil {
		return readFile{}, fmt.Errorf("check the entry: %w", err)
	}

	if entry.GetSize() > githubgit.MaxFileBytes {
		return readFile{}, fmt.Errorf("%w: %s %w", action.ErrPermanent, path, githubgit.ErrTooBig)
	}

	text, err := githubgit.ReadBlob(ctx, r.client, r.owner, r.repo, entry.GetSHA())
	if err != nil {
		if errors.Is(err, githubgit.ErrTooBig) || errors.Is(err, githubgit.ErrNotText) {
			err = fmt.Errorf("%w: %w", action.ErrPermanent, err)
		}

		return readFile{}, fmt.Errorf("read %s: %w", path, err)
	}

	f := readFile{text: text, sha: entry.GetSHA(), mode: entry.GetMode(), exists: true}
	r.files[path] = f

	return f, nil
}

// entry returns the tree entry at path, or nil when there is none. It walks
// the path one directory at a time. A directory on the way that is not a
// directory, a listing that GitHub cut short and a name that differs from an
// entry only in case are errors that wrap action.ErrPermanent.
func (r *reader) entry(ctx context.Context, path string) (*gh.TreeEntry, error) {
	names := strings.Split(path, "/")
	sha := r.root

	for i, name := range names {
		tree, err := r.tree(ctx, sha)
		if err != nil {
			return nil, fmt.Errorf("list the directory above %q: %w", name, err)
		}

		entry, err := childOf(tree, name)
		if err != nil {
			return nil, fmt.Errorf("look for %q: %w: %w", name, action.ErrPermanent, err)
		}

		if entry == nil {
			return nil, nil
		}

		if i == len(names)-1 {
			return entry, nil
		}

		if entry.GetType() != typeTree {
			return nil, fmt.Errorf("%w: %q %w", action.ErrPermanent, name, errThroughFile)
		}

		sha = entry.GetSHA()
	}

	return nil, nil
}

// tree reads one directory, without its subdirectories.
func (r *reader) tree(ctx context.Context, sha string) (*gh.Tree, error) {
	if t, ok := r.trees[sha]; ok {
		return t, nil
	}

	t, _, err := r.client.Git.GetTree(ctx, r.owner, r.repo, sha, false)
	if err != nil {
		return nil, githubgit.Classify("read the tree "+sha, err)
	}

	if t.GetTruncated() {
		return nil, fmt.Errorf("%w: the tree %s %w", action.ErrPermanent, sha, errTreeTooBig)
	}

	r.trees[sha] = t

	return t, nil
}

// childOf returns the entry of the tree called name, or nil. A name that
// matches an entry only when case is ignored is an error: a file system
// that ignores case would treat the two as one file.
func childOf(t *gh.Tree, name string) (*gh.TreeEntry, error) {
	var clash *gh.TreeEntry

	for _, e := range t.Entries {
		switch {
		case e.GetPath() == name:
			return e, nil
		case strings.EqualFold(e.GetPath(), name):
			clash = e
		}
	}

	if clash != nil {
		return nil, fmt.Errorf("%q %w (%q)", name, errCaseClash, clash.GetPath())
	}

	return nil, nil
}

// checkRegular returns an error unless the entry is a regular file, which
// is a blob with the mode 100644 or 100755. A symlink, a submodule and a
// directory are named in the error.
func checkRegular(path string, e *gh.TreeEntry) error {
	switch {
	case e.GetType() == typeTree:
		return fmt.Errorf("%w: path %q is a directory, not a file", action.ErrPermanent, path)
	case e.GetMode() == modeSymlink:
		return fmt.Errorf("%w: path %q is a symlink, which this action does not change", action.ErrPermanent, path)
	case e.GetMode() == modeSubmodule || e.GetType() == typeCommit:
		return fmt.Errorf("%w: path %q is a submodule, which this action does not change", action.ErrPermanent, path)
	case !isRegular(e):
		return fmt.Errorf("%w: path %q is not a regular file (mode %s)", action.ErrPermanent, path, e.GetMode())
	}

	return nil
}

// isRegular reports whether the entry is a blob with the mode 100644 or
// 100755.
func isRegular(e *gh.TreeEntry) bool {
	return e.GetType() == typeBlob && (e.GetMode() == fileMode || e.GetMode() == modeExecutable)
}

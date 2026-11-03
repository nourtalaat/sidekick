package githubrevert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// loaded is a proposal read against the head of the base branch: the commit
// to revert, the files it changed, and what each file is at the head.
type loaded struct {
	params Params
	owner  string
	name   string
	head   githubgit.Base
	target *target
	// files are the files the commit changed, by path.
	files []file
}

// file is a file the commit changed, with what it is at the head of the base
// branch.
type file struct {
	change
	live entry
}

// load checks the parameters against the limits and reads the commit, what
// it changed, and the files at the head of the base branch. It decides
// nothing about the files at the head: Snapshot refuses the ones that
// changed since, and Execute reports them as a conflict. For an undo, the
// commit may be old by now and was checked when it was reverted, so its age,
// its landing on the base branch and its being a revert are not checked
// again, and neither is whether the merge commit holds the whole change.
func (r *Revert) load(ctx context.Context, raw json.RawMessage, undo bool) (*loaded, error) {
	params, err := r.params(raw)
	if err != nil {
		return nil, fmt.Errorf("read the parameters: %w", err)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	head, err := githubgit.BaseOf(ctx, r.client, owner, name, params.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read the base branch: %w", err)
	}

	t, err := r.resolve(ctx, owner, name, params, undo)
	if err != nil {
		return nil, fmt.Errorf("find what to revert: %w", err)
	}

	tr := newTrees(r.client, owner, name)

	changes, err := tr.diff(ctx, t.parentTree, t.commitTree, r.limits.MaxFiles)
	if err != nil {
		return nil, fmt.Errorf("read what %s changed: %w", t.what, err)
	}

	if err := r.checkChanges(changes, t); err != nil {
		return nil, fmt.Errorf("check what %s changed: %w", t.what, err)
	}

	if t.whole != nil && !undo {
		if err := r.proveWhole(ctx, owner, name, t, tr, changes); err != nil {
			return nil, fmt.Errorf("check that the merge commit holds the whole change: %w", err)
		}
	}

	files, err := atHead(ctx, tr, head, changes)
	if err != nil {
		return nil, fmt.Errorf("read the files at the base branch: %w", err)
	}

	return &loaded{params: params, owner: owner, name: name, head: head, target: t, files: files}, nil
}

// checkChanges refuses a commit that changes no file, a path that is refused
// and a file too large to read.
func (r *Revert) checkChanges(changes []change, t *target) error {
	if len(changes) == 0 {
		return refuse("%s changes no file, so there is nothing to revert", t.what)
	}

	for _, c := range changes {
		if err := gitchange.CheckNotRefused(c.path, r.limits.RefusedPaths); err != nil {
			return fmt.Errorf("%s changed it, so it cannot be reverted: %w", t.what, err)
		}

		if c.parent.size > githubgit.MaxFileBytes || c.commit.size > githubgit.MaxFileBytes {
			return refuse("%q is larger than %d bytes; revert it by hand", c.path, githubgit.MaxFileBytes)
		}
	}

	return nil
}

// proveWhole refuses a pull request of several commits whose merge commit
// may not hold the whole change, as holdsWholeChange says. It reads the
// files of the pull request and the point it branched off.
func (r *Revert) proveWhole(ctx context.Context, owner, name string, t *target, tr *trees, changes []change) error {
	files, _, err := r.client.PullRequests.ListFiles(ctx, owner, name, t.whole.number, hundredPerPage)
	if err != nil {
		return githubgit.Classify("list the files of "+t.what, err)
	}

	if len(files) > r.limits.MaxFiles {
		return fmt.Errorf("%s changes more than the %d files one revert may change: %w", t.what, r.limits.MaxFiles, action.ErrLimit)
	}

	var prPaths []string

	for _, f := range files {
		prPaths = append(prPaths, f.GetFilename())

		if f.GetPreviousFilename() != "" {
			prPaths = append(prPaths, f.GetPreviousFilename())
		}
	}

	paths := make([]string, len(changes))
	atParent := make(map[string]entry, len(changes))

	for i, c := range changes {
		paths[i], atParent[c.path] = c.path, c.parent
	}

	if !samePaths(prPaths, paths) {
		return errNotWholeChange
	}

	atMergeBase, err := r.atMergeBase(ctx, owner, name, t, tr, paths)
	if err != nil {
		return fmt.Errorf("read the files where %s branched off: %w", t.what, err)
	}

	return holdsWholeChange(prPaths, paths, atParent, atMergeBase)
}

// atMergeBase reads the paths at the point the pull request branched off
// the base branch: the merge base of the merge commit's parent and the head
// of the pull request.
func (r *Revert) atMergeBase(ctx context.Context, owner, name string, t *target, tr *trees, paths []string) (map[string]entry, error) {
	if t.whole.headSHA == "" {
		return nil, errNotWholeChange
	}

	cmp, _, err := r.client.Repositories.CompareCommits(ctx, owner, name, t.parent, t.whole.headSHA, onePerPage)
	if err != nil {
		return nil, githubgit.Classify("find where "+t.what+" branched off", err)
	}

	base, _, err := r.client.Git.GetCommit(ctx, owner, name, cmp.GetMergeBaseCommit().GetSHA())
	if err != nil {
		return nil, githubgit.Classify("read where "+t.what+" branched off", err)
	}

	at := make(map[string]entry, len(paths))

	for _, path := range paths {
		e, err := tr.at(ctx, base.GetTree().GetSHA(), path)
		if err != nil {
			return nil, fmt.Errorf("read %q where %s branched off: %w", path, t.what, err)
		}

		at[path] = e
	}

	return at, nil
}

// atHead reads each changed file at the head of the base branch.
func atHead(ctx context.Context, tr *trees, head githubgit.Base, changes []change) ([]file, error) {
	files := make([]file, len(changes))

	for i, c := range changes {
		live, err := tr.at(ctx, head.Tree, c.path)
		if err != nil {
			return nil, fmt.Errorf("read %q at the base branch: %w", c.path, err)
		}

		files[i] = file{change: c, live: live}
	}

	return files, nil
}

// texts holds the text of a file now and as the revert leaves it.
type texts struct {
	now     string
	reverts string
}

// readTexts reads the text of the file as it is now (the commit's side) and
// as the revert leaves it (the parent's side). A file that is not there has
// no text.
func (r *Revert) readTexts(ctx context.Context, l *loaded, f file) (texts, error) {
	now, err := r.textOf(ctx, l, f.path, f.commit)
	if err != nil {
		return texts{}, fmt.Errorf("read the file as it is now: %w", err)
	}

	reverts, err := r.textOf(ctx, l, f.path, f.parent)
	if err != nil {
		return texts{}, fmt.Errorf("read the file as the revert leaves it: %w", err)
	}

	return texts{now: now, reverts: reverts}, nil
}

// textOf reads the text of a blob. A file that is not UTF-8 text is
// refused, naming its path.
func (r *Revert) textOf(ctx context.Context, l *loaded, path string, e entry) (string, error) {
	if !e.exists() {
		return "", nil
	}

	text, err := githubgit.ReadBlob(ctx, r.client, l.owner, l.name, e.blob)
	if err != nil {
		if errors.Is(err, githubgit.ErrNotText) || errors.Is(err, githubgit.ErrTooBig) {
			return "", refuse("%q: %w; revert it by hand", path, err)
		}

		return "", fmt.Errorf("read %q: %w", path, err)
	}

	return text, nil
}

// byPath returns the file with the path.
func (l *loaded) byPath(path string) (file, bool) {
	i := slices.IndexFunc(l.files, func(f file) bool { return f.path == path })
	if i < 0 {
		return file{}, false
	}

	return l.files[i], true
}

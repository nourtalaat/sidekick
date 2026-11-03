package gitlabrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// loaded is a proposal read against the head of the base branch: the commit to
// revert, the files it changed, and what each file is at the head.
type loaded struct {
	params Params
	head   gitlabgit.Base
	target *target
	// files are the files the commit changed, by path.
	files []file
}

// file is a file the commit changed: what it is in the commit, in the commit's
// first parent, and at the head of the base branch.
type file struct {
	path   string
	commit gitlabgit.ReadFile
	parent gitlabgit.ReadFile
	live   gitlabgit.ReadFile
}

// load checks the parameters against the limits and reads the commit, what it
// changed, and the files at the head of the base branch. It decides nothing
// about the files at the head: Snapshot refuses the ones that changed since,
// and Execute reports them as a conflict. It writes nothing.
func (r *Revert) load(ctx context.Context, raw json.RawMessage) (*loaded, error) {
	params, err := r.params(raw)
	if err != nil {
		return nil, fmt.Errorf("read the parameters: %w", err)
	}

	head, err := gitlabgit.BaseOf(ctx, r.client, params.Project, params.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read the base branch: %w", err)
	}

	t, err := r.resolve(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("find what to revert: %w", err)
	}

	paths, err := r.changedPaths(ctx, params.Project, t)
	if err != nil {
		return nil, fmt.Errorf("read what %s changed: %w", t.what, err)
	}

	files, err := r.readFiles(ctx, params.Project, head, t, paths)
	if err != nil {
		return nil, fmt.Errorf("read the files of %s: %w", t.what, err)
	}

	return &loaded{params: params, head: head, target: t, files: files}, nil
}

// changedPaths returns the paths the commit changes: a renamed file counts as
// its old path and its new one. It refuses a commit that changes no file or too
// many, a path that is refused, a file GitLab did not diff in full, and a file
// that is a symbolic link or a submodule or changes its mode, none of which the
// action can show or restore by blob.
func (r *Revert) changedPaths(ctx context.Context, project string, t *target) ([]string, error) {
	diffs, err := r.diff(ctx, project, t.sha, r.limits.MaxFiles, t.what)
	if err != nil {
		return nil, fmt.Errorf("read the diff: %w", err)
	}

	if len(diffs) == 0 {
		return nil, refuse("%s changes no file, so there is nothing to revert", t.what)
	}

	var paths []string

	for _, d := range diffs {
		if err := checkDiff(d); err != nil {
			return nil, fmt.Errorf("check %q: %w", d.NewPath, err)
		}

		for _, p := range []string{d.OldPath, d.NewPath} {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}

	if len(paths) > r.limits.MaxFiles {
		return nil, fmt.Errorf("%s changes more than the %d files one revert may change: %w", t.what, r.limits.MaxFiles, action.ErrLimit)
	}

	for _, p := range paths {
		if err := gitchange.CheckNotRefused(p, r.limits.RefusedPaths); err != nil {
			return nil, fmt.Errorf("%s changed it, so it cannot be reverted: %w", t.what, err)
		}
	}

	return paths, nil
}

// readFiles reads each path in the commit, in its first parent and at the head
// of the base branch. A file that is not there is read as one that does not
// exist.
func (r *Revert) readFiles(ctx context.Context, project string, head gitlabgit.Base, t *target, paths []string) ([]file, error) {
	readers := struct{ commit, parent, live *gitlabgit.Reader }{
		commit: gitlabgit.NewReader(r.client, project, t.sha),
		parent: gitlabgit.NewReader(r.client, project, t.parent),
		live:   gitlabgit.NewReader(r.client, project, head.Commit),
	}

	files := make([]file, 0, len(paths))

	for _, p := range paths {
		f := file{path: p}

		for _, read := range []struct {
			reader *gitlabgit.Reader
			into   *gitlabgit.ReadFile
		}{{readers.commit, &f.commit}, {readers.parent, &f.parent}, {readers.live, &f.live}} {
			got, err := read.reader.File(ctx, p)
			if err != nil {
				return nil, fileFailure(p, err)
			}

			*read.into = got
		}

		files = append(files, f)
	}

	return files, nil
}

// checkDiff refuses a file that GitLab left out of the diff, a symbolic link, a
// submodule, and a change of mode.
func checkDiff(d apiDiff) error {
	switch {
	case d.Collapsed || d.TooLarge:
		return refuse("GitLab did not diff %q in full; revert it by hand", d.NewPath)
	case isSpecialMode(d.AMode) || isSpecialMode(d.BMode):
		return refuse("%q is a symbolic link or a submodule; revert it by hand", d.NewPath)
	case d.AMode != d.BMode && d.AMode != "" && d.BMode != "" && d.AMode != modeAbsent && d.BMode != modeAbsent:
		return refuse("%q changes its mode, which this action cannot show; revert it by hand", d.NewPath)
	}

	return nil
}

// isSpecialMode reports whether the mode is a symbolic link or a submodule.
func isSpecialMode(mode string) bool {
	return mode == modeSymlink || mode == modeSubmodule
}

// byPath returns the file with the path.
func (l *loaded) byPath(path string) (file, bool) {
	i := slices.IndexFunc(l.files, func(f file) bool { return f.path == path })
	if i < 0 {
		return file{}, false
	}

	return l.files[i], true
}

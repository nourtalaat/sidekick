package githubpr

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// maxBaseFileBytes is the largest file the action reads from the base
// branch. Monitor definitions are small; a larger file is not one.
const maxBaseFileBytes = githubgit.MaxFileBytes

// Types of the entries of a directory listing and of a file.
const (
	typeFile = "file"
)

var (
	errNotFile = errors.New("is not a regular file")
	errTooBig  = githubgit.ErrTooBig
	errNotText = githubgit.ErrNotText
	errBadBlob = githubgit.ErrBadBlob
)

// reader reads files of one repository at one commit, and remembers what it
// read, so a file that two steps need is fetched once.
type reader struct {
	client *gh.Client
	owner  string
	repo   string
	// ref is the commit SHA every read is pinned to, so the files a
	// validation sees all come from the same state of the branch.
	ref string

	files map[string]*readFile
	dirs  map[string][]gitchange.DirEntry
}

// readFile is the result of reading one path: its text and blob SHA, or
// that it does not exist.
type readFile struct {
	text   string
	sha    string
	exists bool
}

// newReader returns a reader of repository owner/repo at commit ref.
func newReader(client *gh.Client, owner, repo, ref string) *reader {
	return &reader{client: client, owner: owner, repo: repo, ref: ref, files: map[string]*readFile{}, dirs: map[string][]gitchange.DirEntry{}}
}

// file reads the file at path at the reader's commit. A path that does not
// exist at that commit is returned with exists false. A path that is a
// directory, a link or a submodule, a file that is too large, and a file
// that is not text are errors.
func (r *reader) file(ctx context.Context, path string) (readFile, error) {
	if f, ok := r.files[path]; ok {
		return *f, nil
	}

	content, dir, _, err := r.client.Repositories.GetContents(ctx, r.owner, r.repo, path, &gh.RepositoryContentGetOptions{Ref: r.ref})
	if githubgit.IsStatus(err, http.StatusNotFound) {
		r.files[path] = &readFile{}

		return readFile{}, nil
	}

	if err != nil {
		return readFile{}, githubgit.Classify("read "+path, err)
	}

	if content == nil || dir != nil || content.GetType() != typeFile {
		return readFile{}, gitchange.Refuse(fmt.Errorf("%s %w", path, errNotFile))
	}

	if content.GetSize() > maxBaseFileBytes {
		return readFile{}, gitchange.Refuse(fmt.Errorf("%s %w", path, errTooBig))
	}

	text, err := content.GetContent()
	if err != nil {
		return readFile{}, fmt.Errorf("decode %s: %w", path, err)
	}

	if !gitchange.ValidContent(text) {
		return readFile{}, gitchange.Refuse(fmt.Errorf("%s %w", path, errNotText))
	}

	f := &readFile{text: text, sha: content.GetSHA(), exists: true}
	r.files[path] = f

	return *f, nil
}

// Dir lists the regular files directly inside the directory at path; "." is
// the root of the repository. A directory that does not exist has none.
func (r *reader) Dir(ctx context.Context, path string) ([]gitchange.DirEntry, error) {
	if entries, ok := r.dirs[path]; ok {
		return entries, nil
	}

	query := path
	if path == "." {
		query = ""
	}

	content, listing, _, err := r.client.Repositories.GetContents(ctx, r.owner, r.repo, query, &gh.RepositoryContentGetOptions{Ref: r.ref})
	if githubgit.IsStatus(err, http.StatusNotFound) {
		r.dirs[path] = nil

		return nil, nil
	}

	if err != nil {
		return nil, githubgit.Classify("list "+path, err)
	}

	if content != nil {
		return nil, gitchange.Refuse(fmt.Errorf("%s is a file, not a directory", path))
	}

	var entries []gitchange.DirEntry

	for _, e := range listing {
		if e.GetType() != typeFile {
			continue
		}

		entries = append(entries, gitchange.DirEntry{Name: e.GetName(), Path: e.GetPath(), SHA: e.GetSHA(), Size: e.GetSize()})
	}

	r.dirs[path] = entries

	return entries, nil
}

// Blob returns the text of a file of a listing, by its blob SHA.
func (r *reader) Blob(ctx context.Context, e gitchange.DirEntry) (string, error) {
	if f, ok := r.files[e.Path]; ok && f.exists {
		return f.text, nil
	}

	if e.Size > maxBaseFileBytes {
		return "", gitchange.Refuse(fmt.Errorf("%s %w", e.Path, errTooBig))
	}

	raw, _, err := r.client.Git.GetBlobRaw(ctx, r.owner, r.repo, e.SHA)
	if err != nil {
		return "", githubgit.Classify("read "+e.Path, err)
	}

	text := string(raw)
	if !gitchange.ValidContent(text) {
		return "", gitchange.Refuse(fmt.Errorf("%s %w", e.Path, errNotText))
	}

	r.files[e.Path] = &readFile{text: text, sha: e.SHA, exists: true}

	return text, nil
}

package githubpr

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v93/github"
)

// maxBaseFileBytes is the largest file the action reads from the base
// branch. Monitor definitions are small; a larger file is not one.
const maxBaseFileBytes = 256 << 10

// Types of the entries of a directory listing and of a file.
const (
	typeFile = "file"
)

var (
	errNotFile  = errors.New("is not a regular file")
	errTooBig   = fmt.Errorf("is larger than %d bytes", maxBaseFileBytes)
	errNotText  = errors.New("is not a UTF-8 text file")
	errNoSHA    = errors.New("GitHub did not say which commit the branch is at")
	errNoTree   = errors.New("GitHub did not say which tree the commit has")
	errNoNumber = errors.New("GitHub did not return the pull request")
)

// base is the state of the base branch that a pull request is built on.
type base struct {
	// commit and tree are the SHAs of the branch's head commit and its tree.
	commit string
	tree   string
}

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
	dirs  map[string][]dirEntry
}

// readFile is the result of reading one path: its text, or that it does
// not exist.
type readFile struct {
	text   string
	exists bool
}

// dirEntry is a regular file in a directory listing.
type dirEntry struct {
	name string
	path string
	sha  string
	size int
}

// newReader returns a reader of repository owner/repo at commit ref.
func newReader(client *gh.Client, owner, repo, ref string) *reader {
	return &reader{client: client, owner: owner, repo: repo, ref: ref, files: map[string]*readFile{}, dirs: map[string][]dirEntry{}}
}

// baseOf reads the head commit and tree of a branch.
func baseOf(ctx context.Context, client *gh.Client, owner, repo, branch string) (base, error) {
	ref, _, err := client.Git.GetRef(ctx, owner, repo, "heads/"+branch)
	if err != nil {
		return base{}, classify(fmt.Sprintf("read branch %s of %s/%s", branch, owner, repo), err)
	}

	sha := ref.GetObject().GetSHA()
	if sha == "" {
		return base{}, errNoSHA
	}

	commit, _, err := client.Git.GetCommit(ctx, owner, repo, sha)
	if err != nil {
		return base{}, classify("read the head commit of "+branch, err)
	}

	tree := commit.GetTree().GetSHA()
	if tree == "" {
		return base{}, errNoTree
	}

	return base{commit: sha, tree: tree}, nil
}

// file returns the text of the file at path, or exists false when there is
// no such path at the reader's commit. A path that is a directory, a link
// or a submodule, a file that is too large, and a file that is not text
// are errors.
func (r *reader) file(ctx context.Context, path string) (string, bool, error) {
	if f, ok := r.files[path]; ok {
		return f.text, f.exists, nil
	}

	content, dir, _, err := r.client.Repositories.GetContents(ctx, r.owner, r.repo, path, &gh.RepositoryContentGetOptions{Ref: r.ref})
	if isStatus(err, http.StatusNotFound) {
		r.files[path] = &readFile{}

		return "", false, nil
	}

	if err != nil {
		return "", false, classify("read "+path, err)
	}

	if content == nil || dir != nil || content.GetType() != typeFile {
		return "", false, fmt.Errorf("%s %w", path, errNotFile)
	}

	if content.GetSize() > maxBaseFileBytes {
		return "", false, fmt.Errorf("%s %w", path, errTooBig)
	}

	text, err := content.GetContent()
	if err != nil {
		return "", false, fmt.Errorf("decode %s: %w", path, err)
	}

	if !validContent(text) {
		return "", false, fmt.Errorf("%s %w", path, errNotText)
	}

	r.files[path] = &readFile{text: text, exists: true}

	return text, true, nil
}

// dir lists the regular files directly inside the directory at path; "." is
// the root of the repository. A directory that does not exist has none.
func (r *reader) dir(ctx context.Context, path string) ([]dirEntry, error) {
	if entries, ok := r.dirs[path]; ok {
		return entries, nil
	}

	query := path
	if path == "." {
		query = ""
	}

	content, listing, _, err := r.client.Repositories.GetContents(ctx, r.owner, r.repo, query, &gh.RepositoryContentGetOptions{Ref: r.ref})
	if isStatus(err, http.StatusNotFound) {
		r.dirs[path] = nil

		return nil, nil
	}

	if err != nil {
		return nil, classify("list "+path, err)
	}

	if content != nil {
		return nil, fmt.Errorf("%s is a file, not a directory", path)
	}

	var entries []dirEntry

	for _, e := range listing {
		if e.GetType() != typeFile {
			continue
		}

		entries = append(entries, dirEntry{name: e.GetName(), path: e.GetPath(), sha: e.GetSHA(), size: e.GetSize()})
	}

	r.dirs[path] = entries

	return entries, nil
}

// blob returns the text of a file of a listing, by its blob SHA.
func (r *reader) blob(ctx context.Context, e dirEntry) (string, error) {
	if f, ok := r.files[e.path]; ok && f.exists {
		return f.text, nil
	}

	if e.size > maxBaseFileBytes {
		return "", fmt.Errorf("%s %w", e.path, errTooBig)
	}

	raw, _, err := r.client.Git.GetBlobRaw(ctx, r.owner, r.repo, e.sha)
	if err != nil {
		return "", classify("read "+e.path, err)
	}

	text := string(raw)
	if !validContent(text) {
		return "", fmt.Errorf("%s %w", e.path, errNotText)
	}

	r.files[e.path] = &readFile{text: text, exists: true}

	return text, nil
}

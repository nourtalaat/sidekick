package gitlabmr

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// Limits of what the action reads.
const (
	// maxBaseFileBytes is the largest file the action reads from the base
	// branch. Monitor definitions are small; a larger file is not one.
	maxBaseFileBytes = 256 << 10
	// maxBodyBytes bounds the answer for one file or blob. GitLab sends the
	// content as base64 inside JSON, a third larger than the file.
	maxBodyBytes = 2 * maxBaseFileBytes
	// treePageSize is how many entries one page of a directory listing asks
	// for, the most GitLab sends.
	treePageSize = 100
	// maxTreePages bounds the pages read for one directory.
	maxTreePages = 10
	// sha1Length is the length of a Git blob ID in hex. GitLab repositories
	// that use SHA-256 have longer ones.
	sha1Length = 40
	// symlinkMode is the mode GitLab gives a symbolic link in a listing.
	symlinkMode = "120000"
)

// Types of the entries of a directory listing.
const (
	entryBlob = "blob"
)

var (
	errNotFile         = errors.New("is not a regular file")
	errTooBig          = fmt.Errorf("is larger than %d bytes", maxBaseFileBytes)
	errNotText         = errors.New("is not a UTF-8 text file")
	errNoSHA           = errors.New("GitLab did not say which commit the branch is at")
	errUnsupportedHash = errors.New("has a blob ID that is not a SHA-1 hash, and repositories that use SHA-256 are not supported")
	errTooManyEntries  = fmt.Errorf("has more than %d entries", maxTreePages*treePageSize)
	errNotBase64       = errors.New("GitLab did not send the content as base64")
)

// base is the state of the base branch that a merge request is built on.
type base struct {
	// commit is the SHA of the branch's head commit.
	commit string
}

// reader reads files of one project at one commit, and remembers what it
// read, so a file that two steps need is fetched once.
type reader struct {
	client  *gitlabapi.Client
	project string
	// ref is the commit SHA every read is pinned to, so the files a
	// validation sees all come from the same state of the branch.
	ref string

	files map[string]*readFile
	trees map[string][]apiTreeEntry
}

// readFile is the result of reading one path: its text and blob ID, or that
// it does not exist.
type readFile struct {
	text   string
	sha    string
	exists bool
}

// apiBranch is a branch as GitLab's branches endpoint answers.
type apiBranch struct {
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// apiFile is a file as GitLab's repository files endpoint answers.
type apiFile struct {
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	BlobID   string `json:"blob_id"`
}

// apiBlob is a blob as GitLab's blobs endpoint answers.
type apiBlob struct {
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// apiTreeEntry is an entry of a directory as GitLab's tree endpoint answers.
type apiTreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// newReader returns a reader of project at commit ref.
func newReader(client *gitlabapi.Client, project, ref string) *reader {
	return &reader{client: client, project: project, ref: ref, files: map[string]*readFile{}, trees: map[string][]apiTreeEntry{}}
}

// baseOf reads the head commit of a branch.
func baseOf(ctx context.Context, client *gitlabapi.Client, project, branch string) (base, error) {
	var b apiBranch

	if _, err := client.Get(ctx, branchPath(project, branch), nil, &b); err != nil {
		return base{}, classify(fmt.Sprintf("read branch %s of %s", branch, project), err)
	}

	if b.Commit.ID == "" {
		return base{}, errNoSHA
	}

	return base{commit: b.Commit.ID}, nil
}

// file reads the file at path at the reader's commit. A path that does not
// exist at that commit is returned with exists false. A path that is a
// directory, a file that is too large, and a file that is not text are
// errors.
func (r *reader) file(ctx context.Context, p string) (readFile, error) {
	if f, ok := r.files[p]; ok {
		return *f, nil
	}

	var file apiFile

	api := gitlabapi.ProjectPath(r.project, "repository", "files", url.PathEscape(p))

	_, err := r.client.GetLimited(ctx, api, url.Values{"ref": {r.ref}}, &file, maxBodyBytes)

	switch {
	case gitlabapi.IsStatus(err, http.StatusNotFound):
		return r.missing(ctx, p)
	case errors.Is(err, gitlabapi.ErrResponseTooLarge):
		return readFile{}, fmt.Errorf("%s %w", p, errTooBig)
	case err != nil:
		return readFile{}, classify("read "+p, err)
	}

	text, err := decodeText(p, file.Encoding, file.Content, file.Size)
	if err != nil {
		return readFile{}, fmt.Errorf("read %s: %w", p, err)
	}

	if len(file.BlobID) != sha1Length {
		return readFile{}, fmt.Errorf("%s %w", p, errUnsupportedHash)
	}

	f := &readFile{text: text, sha: file.BlobID, exists: true}
	r.files[p] = f

	return *f, nil
}

// missing records that p has no file at the commit. GitLab answers 404 for a
// directory too, and writing a file where a directory is would replace it, so
// a name that is a directory in the listing of its parent is an error.
func (r *reader) missing(ctx context.Context, p string) (readFile, error) {
	entries, err := r.tree(ctx, parentOf(p))
	if err != nil {
		return readFile{}, fmt.Errorf("list the directory of %s: %w", p, err)
	}

	for _, e := range entries {
		if e.Path == p {
			return readFile{}, fmt.Errorf("%s %w", p, errNotFile)
		}
	}

	r.files[p] = &readFile{}

	return readFile{}, nil
}

// Dir lists the regular files directly inside the directory at path; "." is
// the root of the project. A directory that does not exist has none.
func (r *reader) Dir(ctx context.Context, p string) ([]gitchange.DirEntry, error) {
	entries, err := r.tree(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list the directory %s: %w", p, err)
	}

	var files []gitchange.DirEntry

	for _, e := range entries {
		if e.Type != entryBlob || e.Mode == symlinkMode {
			continue
		}

		files = append(files, gitchange.DirEntry{Name: e.Name, Path: e.Path, SHA: e.ID})
	}

	return files, nil
}

// Blob returns the text of a file of a listing, by its blob ID. GitLab's
// listing has no sizes, so the size is checked when the blob is read.
func (r *reader) Blob(ctx context.Context, e gitchange.DirEntry) (string, error) {
	if f, ok := r.files[e.Path]; ok && f.exists {
		return f.text, nil
	}

	text, err := readBlobText(ctx, r.client, r.project, e.SHA, e.Path)
	if err != nil {
		return "", fmt.Errorf("read the blob of %s: %w", e.Path, err)
	}

	r.files[e.Path] = &readFile{text: text, sha: e.SHA, exists: true}

	return text, nil
}

// tree lists every entry directly inside the directory at path, files and
// directories, at the reader's commit. "." is the root of the project. A
// directory that does not exist has none.
func (r *reader) tree(ctx context.Context, dir string) ([]apiTreeEntry, error) {
	if entries, ok := r.trees[dir]; ok {
		return entries, nil
	}

	query := url.Values{"ref": {r.ref}}
	if dir != "." {
		query.Set("path", dir)
	}

	api := gitlabapi.ProjectPath(r.project, "repository", "tree")

	var entries []apiTreeEntry

	for page := 1; page > 0; {
		if page > maxTreePages {
			return nil, fmt.Errorf("%s %w", dir, errTooManyEntries)
		}

		items, next, err := gitlabapi.ListPage[apiTreeEntry](ctx, r.client, api, query, page, treePageSize)
		if gitlabapi.IsStatus(err, http.StatusNotFound) {
			r.trees[dir] = nil

			return nil, nil
		}

		if err != nil {
			return nil, classify("list "+dir, err)
		}

		entries, page = append(entries, items...), next
	}

	r.trees[dir] = entries

	return entries, nil
}

// readBlobText reads a blob by its ID and returns its text. A blob that is
// too large or is not UTF-8 text is an error. name says what the blob is, for
// the errors.
func readBlobText(ctx context.Context, client *gitlabapi.Client, project, blob, name string) (string, error) {
	var b apiBlob

	api := gitlabapi.ProjectPath(project, "repository", "blobs", url.PathEscape(blob))

	if _, err := client.GetLimited(ctx, api, nil, &b, maxBodyBytes); err != nil {
		if errors.Is(err, gitlabapi.ErrResponseTooLarge) {
			return "", fmt.Errorf("%s %w", name, errTooBig)
		}

		return "", classify("read "+name, err)
	}

	return decodeText(name, b.Encoding, b.Content, b.Size)
}

// decodeText decodes the base64 content of a file or blob and checks it:
// within the size limit, UTF-8 text, and the size GitLab says. GitLab
// converts text that is not stored as UTF-8 before it encodes it, and then
// the size of the stored blob differs from the length of the content.
func decodeText(name, encoding, content string, size int) (string, error) {
	if size > maxBaseFileBytes {
		return "", fmt.Errorf("%s %w", name, errTooBig)
	}

	if encoding != "base64" {
		return "", fmt.Errorf("%s: %w", name, errNotBase64)
	}

	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", name, err)
	}

	text := string(raw)
	if len(raw) != size || !gitchange.ValidContent(text) {
		return "", fmt.Errorf("%s %w", name, errNotText)
	}

	return text, nil
}

// branchPath returns the API path of a branch of a project.
func branchPath(project, branch string) string {
	return gitlabapi.ProjectPath(project, "repository", "branches", url.PathEscape(branch))
}

// parentOf returns the directory of a path, "." for the root.
func parentOf(p string) string {
	return path.Dir(p)
}

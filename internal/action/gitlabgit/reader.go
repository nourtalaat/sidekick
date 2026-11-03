package gitlabgit

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

// Limits of what an action reads.
const (
	// MaxFileBytes is the largest file an action reads from a project.
	MaxFileBytes = 256 << 10
	// maxBodyBytes bounds the answer for one file or blob. GitLab sends the
	// content as base64 inside JSON, a third larger than the file.
	maxBodyBytes = 2 * MaxFileBytes
	// treePageSize is how many entries one page of a directory listing asks
	// for, the most GitLab sends.
	treePageSize = 100
	// maxTreePages bounds the pages read for one directory.
	maxTreePages = 10
	// sha1Length is the length of a Git blob ID in hex. GitLab repositories
	// that use SHA-256 have longer ones.
	sha1Length = 40
	// ModeSymlink is the mode GitLab gives a symbolic link in a listing.
	ModeSymlink = "120000"
)

// EntryBlob is the type of a file in a directory listing.
const EntryBlob = "blob"

// The errors of reading a file or a directory. Each continues a sentence that
// starts with the path.
var (
	ErrNotFile         = errors.New("is not a regular file")
	ErrTooBig          = fmt.Errorf("is larger than %d bytes", MaxFileBytes)
	ErrNotText         = errors.New("is not a UTF-8 text file")
	ErrUnsupportedHash = errors.New("has a blob ID that is not a SHA-1 hash, and repositories that use SHA-256 are not supported")
	ErrTooManyEntries  = fmt.Errorf("has more than %d entries", maxTreePages*treePageSize)
	ErrNotBase64       = errors.New("GitLab did not send the content as base64")
)

// Reader reads files of one project at one commit, and remembers what it
// read, so a file that two steps need is fetched once.
type Reader struct {
	client  *gitlabapi.Client
	project string
	// ref is the commit SHA every read is pinned to, so the files a
	// validation sees all come from the same state of the branch.
	ref string

	files map[string]*ReadFile
	trees map[string][]TreeEntry
}

// ReadFile is the result of reading one path: its text and blob ID, or that
// it does not exist.
type ReadFile struct {
	Text   string
	SHA    string
	Exists bool
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

// TreeEntry is an entry of a directory as GitLab's tree endpoint answers.
type TreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// NewReader returns a reader of project at commit ref.
func NewReader(client *gitlabapi.Client, project, ref string) *Reader {
	return &Reader{client: client, project: project, ref: ref, files: map[string]*ReadFile{}, trees: map[string][]TreeEntry{}}
}

// File reads the file at path at the reader's commit. A path that does not
// exist at that commit is returned with exists false. A path that is a
// directory, a file that is too large, and a file that is not text are
// errors.
func (r *Reader) File(ctx context.Context, p string) (ReadFile, error) {
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
		return ReadFile{}, fmt.Errorf("%s %w", p, ErrTooBig)
	case err != nil:
		return ReadFile{}, Classify("read "+p, err)
	}

	text, err := decodeText(p, file.Encoding, file.Content, file.Size)
	if err != nil {
		return ReadFile{}, fmt.Errorf("read %s: %w", p, err)
	}

	if len(file.BlobID) != sha1Length {
		return ReadFile{}, fmt.Errorf("%s %w", p, ErrUnsupportedHash)
	}

	f := &ReadFile{Text: text, SHA: file.BlobID, Exists: true}
	r.files[p] = f

	return *f, nil
}

// missing records that p has no file at the commit. GitLab answers 404 for a
// directory too, and writing a file where a directory is would replace it, so
// a name that is a directory in the listing of its parent is an error.
func (r *Reader) missing(ctx context.Context, p string) (ReadFile, error) {
	entries, err := r.Tree(ctx, parentOf(p))
	if err != nil {
		return ReadFile{}, fmt.Errorf("list the directory of %s: %w", p, err)
	}

	for _, e := range entries {
		if e.Path == p {
			return ReadFile{}, fmt.Errorf("%s %w", p, ErrNotFile)
		}
	}

	r.files[p] = &ReadFile{}

	return ReadFile{}, nil
}

// Dir lists the regular files directly inside the directory at path; "." is
// the root of the project. A directory that does not exist has none.
func (r *Reader) Dir(ctx context.Context, p string) ([]gitchange.DirEntry, error) {
	entries, err := r.Tree(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("list the directory %s: %w", p, err)
	}

	var files []gitchange.DirEntry

	for _, e := range entries {
		if e.Type != EntryBlob || e.Mode == ModeSymlink {
			continue
		}

		files = append(files, gitchange.DirEntry{Name: e.Name, Path: e.Path, SHA: e.ID})
	}

	return files, nil
}

// Blob returns the text of a file of a listing, by its blob ID. GitLab's
// listing has no sizes, so the size is checked when the blob is read.
func (r *Reader) Blob(ctx context.Context, e gitchange.DirEntry) (string, error) {
	if f, ok := r.files[e.Path]; ok && f.Exists {
		return f.Text, nil
	}

	text, err := ReadBlobText(ctx, r.client, r.project, e.SHA, e.Path)
	if err != nil {
		return "", fmt.Errorf("read the blob of %s: %w", e.Path, err)
	}

	r.files[e.Path] = &ReadFile{Text: text, SHA: e.SHA, Exists: true}

	return text, nil
}

// Tree lists every entry directly inside the directory at path, files and
// directories, at the reader's commit. "." is the root of the project. A
// directory that does not exist has none.
func (r *Reader) Tree(ctx context.Context, dir string) ([]TreeEntry, error) {
	if entries, ok := r.trees[dir]; ok {
		return entries, nil
	}

	query := url.Values{"ref": {r.ref}}
	if dir != "." {
		query.Set("path", dir)
	}

	api := gitlabapi.ProjectPath(r.project, "repository", "tree")

	var entries []TreeEntry

	for page := 1; page > 0; {
		if page > maxTreePages {
			return nil, fmt.Errorf("%s %w", dir, ErrTooManyEntries)
		}

		items, next, err := gitlabapi.ListPage[TreeEntry](ctx, r.client, api, query, page, treePageSize)
		if gitlabapi.IsStatus(err, http.StatusNotFound) {
			r.trees[dir] = nil

			return nil, nil
		}

		if err != nil {
			return nil, Classify("list "+dir, err)
		}

		entries, page = append(entries, items...), next
	}

	r.trees[dir] = entries

	return entries, nil
}

// decodeText decodes the base64 content of a file or blob and checks it:
// within the size limit, UTF-8 text, and the size GitLab says. GitLab
// converts text that is not stored as UTF-8 before it encodes it, and then
// the size of the stored blob differs from the length of the content.
func decodeText(name, encoding, content string, size int) (string, error) {
	if size > MaxFileBytes {
		return "", fmt.Errorf("%s %w", name, ErrTooBig)
	}

	if encoding != "base64" {
		return "", fmt.Errorf("%s: %w", name, ErrNotBase64)
	}

	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", name, err)
	}

	text := string(raw)
	if len(raw) != size || !gitchange.ValidContent(text) {
		return "", fmt.Errorf("%s %w", name, ErrNotText)
	}

	return text, nil
}

// parentOf returns the directory of a path, "." for the root.
func parentOf(p string) string {
	return path.Dir(p)
}

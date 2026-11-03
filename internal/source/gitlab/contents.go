package gitlab

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of get_file.
const (
	// maxFileSize is the most file text a result keeps.
	maxFileSize = 256 << 10
	// maxFileBytes is the largest file whose text is read; a larger file is
	// left out.
	maxFileBytes = 1 << 20
	// maxFileBody bounds the answer for one file. GitLab sends the content
	// as base64 inside JSON, so the answer for a file of maxFileBytes is
	// about a third larger than the file.
	maxFileBody = 2 << 20
	// maxEntries is the most directory entries a result keeps.
	maxEntries = 1000
)

// Reasons get_file leaves a file's content out.
const (
	omittedBinary       = "binary file"
	omittedFileTooLarge = "file over 1 MB"
)

// apiFile is a file as GitLab's repository files endpoint answers.
type apiFile struct {
	FilePath string `json:"file_path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	BlobID   string `json:"blob_id"`
	CommitID string `json:"commit_id"`
}

// apiTreeEntry is an entry of a directory as GitLab's tree endpoint answers.
type apiTreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// fileResult is the result of get_file: a file's content, or the entries
// of a directory.
type fileResult struct {
	Project string `json:"project"`
	Path    string `json:"path"`
	Ref     string `json:"ref,omitempty"`
	Type    string `json:"type"`
	SHA     string `json:"sha,omitempty"`
	Size    int    `json:"size,omitempty"`
	Content string `json:"content,omitempty"`
	// Omitted says why the content is not there.
	Omitted string      `json:"omitted,omitempty"`
	Entries []fileEntry `json:"entries,omitempty"`
	URL     string      `json:"url,omitempty"`
}

// fileEntry is one entry of a directory.
type fileEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// getFile runs get_file. A path is read as a file first and, when GitLab has
// no such file, as a directory, because the two have different endpoints.
func (s *Source) getFile(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Path string `json:"path"`
		Ref  string `json:"ref"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	p, err := cleanPath("path", in.Path)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := checkRef("ref", in.Ref); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if p == "" {
		return s.directory(ctx, project, p, in.Ref)
	}

	file, err := s.readFile(ctx, project, p, in.Ref)

	out := fileResult{Project: project, Path: p, Ref: in.Ref, Type: "file"}

	switch {
	case isNotFound(err):
		return s.directory(ctx, project, p, in.Ref)
	case errors.Is(err, errResponseTooLarge):
		out.Omitted = omittedFileTooLarge

		return structured(out, nil, true, false)
	case err != nil:
		return source.Result{}, fmt.Errorf("get %s of %s: %w", p, project, err)
	}

	out.SHA, out.Size = file.BlobID, file.Size

	truncated, err := out.setContent(file)
	if err != nil {
		return source.Result{}, fmt.Errorf("read %s of %s: %w", p, project, err)
	}

	out.URL = s.fileURL(project, cmp.Or(file.CommitID, in.Ref, "HEAD"), p)
	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: fileTitle(project, p, in.Ref), URL: out.URL}}

	return structured(out, evidence, truncated, false)
}

// readFile reads one file at a ref, or at the default branch for no ref.
func (s *Source) readFile(ctx context.Context, project, p, ref string) (apiFile, error) {
	if ref == "" {
		ref = "HEAD"
	}

	var file apiFile

	path := projectPath(project, "repository", "files", url.PathEscape(p))
	if _, err := s.client.getLimited(ctx, path, url.Values{"ref": {ref}}, &file, maxFileBody); err != nil {
		return apiFile{}, fmt.Errorf("read the file: %w", err)
	}

	return file, nil
}

// directory returns the result of get_file for a directory, which is the
// root of the project when p is empty.
func (s *Source) directory(ctx context.Context, project, p, ref string) (source.Result, error) {
	query := url.Values{}
	setIf(query, "path", p)
	setIf(query, "ref", ref)

	path := projectPath(project, "repository", "tree")

	entries, more, err := collect(ctx, maxEntries, func(ctx context.Context, page int) ([]apiTreeEntry, int, error) {
		return listPage[apiTreeEntry](ctx, s.client, path, query, page, s.pageSize)
	}, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("list %s of %s: %w", cmp.Or(p, "the root"), project, err)
	}

	out := fileResult{Project: project, Path: p, Ref: ref, Type: "dir"}

	for _, e := range entries {
		out.Entries = append(out.Entries, fileEntry{Name: e.Name, Type: entryType(e.Type)})
	}

	return structured(out, nil, more, len(out.Entries) == 0)
}

// setContent sets the text of a file, cut to maxFileSize, or says why it is
// left out. It reports whether the text was cut or left out.
func (f *fileResult) setContent(file apiFile) (bool, error) {
	if file.Size > maxFileBytes {
		f.Omitted = omittedFileTooLarge

		return true, nil
	}

	if file.Encoding != "base64" {
		return false, fmt.Errorf("GitLab sent the content as %q, not base64", file.Encoding)
	}

	raw, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return false, fmt.Errorf("decode the content: %w", err)
	}

	text := string(raw)
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		f.Omitted = omittedBinary

		return false, nil
	}

	f.Content = source.CutText(text, maxFileSize)

	return len(f.Content) < len(text), nil
}

// fileURL returns the link to a file's page in GitLab at a commit or ref.
func (s *Source) fileURL(project, ref, p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	return s.cfg.baseURL() + "/" + project + "/-/blob/" + ref + "/" + strings.Join(parts, "/")
}

// isNotFound reports whether err is GitLab's 404.
func isNotFound(err error) bool {
	se, ok := errors.AsType[*statusError](err)

	return ok && se.code == http.StatusNotFound
}

// entryType returns the kind of a directory entry as GitLab's tree endpoint
// names it, in the words get_file uses.
func entryType(t string) string {
	switch t {
	case "blob":
		return "file"
	case "tree":
		return "dir"
	case "commit":
		return "submodule"
	default:
		return t
	}
}

// fileTitle returns the evidence title of a file.
func fileTitle(project, p, ref string) string {
	title := project + ":" + p
	if ref != "" {
		title += "@" + ref
	}

	return title
}

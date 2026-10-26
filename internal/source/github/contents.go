package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of get_file.
const (
	// maxFileSize is the most file text a result keeps.
	maxFileSize = 256 << 10
	// maxEntries is the most directory entries a result keeps, which is
	// also the most GitHub sends.
	maxEntries = 1000
)

// Reasons get_file leaves a file's content out.
const (
	omittedBinary   = "binary file"
	omittedTooLarge = "too large for the GitHub contents API"
)

// fileResult is the result of get_file: a file's content, or the entries
// of a directory.
type fileResult struct {
	Repo    string `json:"repo"`
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
	Size int    `json:"size,omitempty"`
}

// getFile runs get_file.
func (s *Source) getFile(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Path string `json:"path"`
		Ref  string `json:"ref"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	r, err := s.resolve(in.target)
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

	file, dir, _, err := s.client.Repositories.GetContents(ctx, r.owner, r.name, p, &gh.RepositoryContentGetOptions{Ref: in.Ref})
	if err != nil {
		return source.Result{}, fmt.Errorf("get %s of %s: %w", p, r, err)
	}

	if dir != nil {
		return directory(r, p, in.Ref, dir)
	}

	out := fileResult{
		Repo: r.String(),
		Path: file.GetPath(),
		Ref:  in.Ref,
		Type: file.GetType(),
		SHA:  file.GetSHA(),
		Size: file.GetSize(),
		URL:  file.GetHTMLURL(),
	}

	truncated := false

	if out.Type == "file" {
		truncated, err = out.setContent(file)
		if err != nil {
			return source.Result{}, fmt.Errorf("read %s of %s: %w", p, r, err)
		}
	}

	evidence := []source.Evidence{{Kind: source.EvidenceResource, Title: fileTitle(r, out.Path, in.Ref), URL: out.URL}}

	return structured(out, evidence, truncated, false)
}

// setContent sets the text of a file, cut to maxFileSize, or says why it is
// left out. It reports whether the text was cut.
func (f *fileResult) setContent(file *gh.RepositoryContent) (bool, error) {
	if file.GetEncoding() == "none" || (file.Content == nil && f.Size > 0) {
		f.Omitted = omittedTooLarge

		return true, nil
	}

	text, err := file.GetContent()
	if err != nil {
		return false, fmt.Errorf("decode the content: %w", err)
	}

	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		f.Omitted = omittedBinary

		return false, nil
	}

	f.Content = source.CutText(text, maxFileSize)

	return len(f.Content) < len(text), nil
}

// directory returns the result of get_file for a directory.
func directory(r repo, p, ref string, entries []*gh.RepositoryContent) (source.Result, error) {
	out := fileResult{Repo: r.String(), Path: p, Ref: ref, Type: "dir"}

	for _, e := range entries[:min(len(entries), maxEntries)] {
		out.Entries = append(out.Entries, fileEntry{Name: e.GetName(), Type: e.GetType(), Size: e.GetSize()})
	}

	return structured(out, nil, len(entries) > maxEntries, len(out.Entries) == 0)
}

// fileTitle returns the evidence title of a file.
func fileTitle(r repo, p, ref string) string {
	title := r.String() + ":" + p
	if ref != "" {
		title += "@" + ref
	}

	return title
}

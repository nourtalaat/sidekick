package gitchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Limits on the parameters that do not depend on the config.
const (
	// MaxTitleChars is the longest pull request title.
	MaxTitleChars = 120
	// maxBodyChars is the longest description the model may write.
	maxBodyChars = 8000
	// maxPathBytes is the longest file path.
	maxPathBytes = 512
	// gitDir is the name of a directory no path may go through.
	gitDir = ".git"
)

var (
	errBadPath    = errors.New("not a clean path inside the repository")
	errBothOrNone = errors.New("give either content or delete")
)

// Edit is the part of the parameters of a pull request action that is the same
// for every host: the branch, the text and the files. A host puts it in its own
// Params type, next to the field that names the repository or project, and
// embeds it so the JSON stays flat.
type Edit struct {
	// BaseBranch is the branch the pull request targets.
	BaseBranch string `json:"baseBranch"`
	// Title and Body are the pull request's.
	Title string `json:"title"`
	Body  string `json:"body"`
	// Files are the changes, each the whole new content of a file or a
	// delete.
	Files []FileChange `json:"files"`
}

// FileChange is one file the pull request writes or deletes.
type FileChange struct {
	Path string `json:"path"`
	// Content is the whole new content. Nil for a delete.
	Content *string `json:"content,omitempty"`
	Delete  bool    `json:"delete,omitempty"`
}

// PlannedFile is a FileChange that passed the limits, with the format its
// path is checked as.
type PlannedFile struct {
	Path    string
	Format  validateclient.Format
	Content *string
}

// Deleted reports whether the change removes the file.
func (f PlannedFile) Deleted() bool {
	return f.Content == nil
}

// ParamsSchema returns the JSON Schema of a host's Params: an Edit and one
// more required string, the repository or project, under targetKey. Sidekick
// checks the parameters against it before it calls Validate, and the model
// reads it. Both arguments must be plain text, written into the schema as it
// is.
func ParamsSchema(targetKey, targetDescription string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": [%[1]s, "baseBranch", "title", "body", "files"],
  "properties": {
    %[1]s: {"type": "string", "description": %[2]s},
    "baseBranch": {"type": "string", "description": "The branch the pull request targets."},
    "title": {"type": "string", "minLength": 1, "maxLength": 120},
    "body": {"type": "string", "maxLength": 8000, "description": "The pull request description: what changes and why."},
    "files": {
      "type": "array",
      "minItems": 1,
      "maxItems": %[3]d,
      "description": "The files to change. Give the whole new content of each, or delete it.",
      "items": {
        "oneOf": [
          {
            "type": "object",
            "additionalProperties": false,
            "required": ["path", "content"],
            "properties": {
              "path": {"type": "string", "description": "The path from the root of the repository."},
              "content": {"type": "string", "description": "The whole new content of the file."}
            }
          },
          {
            "type": "object",
            "additionalProperties": false,
            "required": ["path", "delete"],
            "properties": {
              "path": {"type": "string", "description": "The path from the root of the repository."},
              "delete": {"type": "boolean", "const": true}
            }
          }
        ]
      }
    }
  }
}`, strconv.Quote(targetKey), strconv.Quote(targetDescription), MaxFilesCeiling))
}

// DecodeParams decodes the parameters into a host's Params type, refusing
// unknown fields.
func DecodeParams[P any](raw json.RawMessage) (P, error) {
	var p P

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&p); err != nil {
		var zero P

		return zero, fmt.Errorf("decode the parameters: %w", err)
	}

	return p, nil
}

// Plan checks the edit against the bounds and returns the files with the
// format of each. formatOf gives the format a path is checked as, or an
// error when the path is not allowed; its error is put after the path. The
// host checks its own target, the repository or project, before it calls
// Plan. A limit that Plan finds broken wraps action.ErrLimit.
func Plan(e Edit, b Bounds, formatOf func(path string) (validateclient.Format, error)) ([]PlannedFile, error) {
	if e.BaseBranch != b.BaseBranch {
		return nil, fmt.Errorf("baseBranch %q is not the allowed base branch %q: %w", e.BaseBranch, b.BaseBranch, action.ErrLimit)
	}

	if err := checkText(e); err != nil {
		return nil, fmt.Errorf("check the title and body: %w", err)
	}

	if len(e.Files) == 0 {
		return nil, errors.New("files: give at least one file")
	}

	if len(e.Files) > b.MaxFiles {
		return nil, fmt.Errorf("%d files, the most one pull request may change is %d: %w", len(e.Files), b.MaxFiles, action.ErrLimit)
	}

	files := make([]PlannedFile, 0, len(e.Files))
	seen := make(map[string]bool, len(e.Files))

	for _, f := range e.Files {
		planned, err := planFile(f, formatOf)
		if err != nil {
			return nil, fmt.Errorf("check the files: %w", err)
		}

		key := strings.ToLower(planned.Path)
		if seen[key] {
			return nil, fmt.Errorf("path %q is given twice", planned.Path)
		}

		seen[key] = true

		files = append(files, planned)
	}

	return files, nil
}

// planFile checks one file.
func planFile(f FileChange, formatOf func(path string) (validateclient.Format, error)) (PlannedFile, error) {
	if err := checkPath(f.Path); err != nil {
		return PlannedFile{}, fmt.Errorf("path %q: %w", f.Path, err)
	}

	format, err := formatOf(f.Path)
	if err != nil {
		return PlannedFile{}, fmt.Errorf("path %q %w", f.Path, err)
	}

	if hasContent := f.Content != nil; hasContent == f.Delete {
		return PlannedFile{}, fmt.Errorf("path %q: %w", f.Path, errBothOrNone)
	}

	if f.Content != nil && !ValidContent(*f.Content) {
		return PlannedFile{}, fmt.Errorf("path %q: content must be valid UTF-8 text without NUL characters", f.Path)
	}

	return PlannedFile{Path: f.Path, Format: format, Content: f.Content}, nil
}

// checkText checks the title and the body.
func checkText(p Edit) error {
	switch {
	case strings.TrimSpace(p.Title) == "":
		return errors.New("title: required")
	case len([]rune(p.Title)) > MaxTitleChars:
		return fmt.Errorf("title: longer than %d characters", MaxTitleChars)
	case len([]rune(p.Body)) > maxBodyChars:
		return fmt.Errorf("body: longer than %d characters", maxBodyChars)
	case !ValidContent(p.Title) || !ValidContent(p.Body):
		return errors.New("title and body must be valid UTF-8 text without NUL characters")
	}

	return nil
}

// checkPath checks that p is a clean path from the root of the repository:
// valid text with no control characters or backslashes, no empty, dotted or
// .git segment, and not longer than maxPathBytes.
func checkPath(p string) error {
	if p == "" || len(p) > maxPathBytes || !utf8.ValidString(p) || path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return errBadPath
	}

	for _, r := range p {
		if r < ' ' || r == 0x7f || r == '\\' {
			return errBadPath
		}
	}

	for seg := range strings.SplitSeq(p, "/") {
		if seg == ".." || seg == "." || strings.EqualFold(seg, gitDir) {
			return errBadPath
		}
	}

	return nil
}

// ValidContent reports whether s is text Git can store as it is and
// Postgres can keep: valid UTF-8 without NUL.
func ValidContent(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

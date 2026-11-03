package githubpr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// Limits on the parameters that do not depend on the config.
const (
	// maxTitleChars is the longest pull request title.
	maxTitleChars = 120
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

// Params are what the model fills in when it proposes the action.
type Params struct {
	// Repo is the repository, as owner/name.
	Repo string `json:"repo"`
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

// change is a FileChange that passed the limits, with the format its path
// is checked as.
type change struct {
	path    string
	format  validateclient.Format
	content *string
}

// deleted reports whether the change removes the file.
func (c change) deleted() bool {
	return c.content == nil
}

// paramsSchema is the JSON Schema of Params. Sidekick checks the parameters
// against it before it calls Validate, and the model reads it.
var paramsSchema = json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["repo", "baseBranch", "title", "body", "files"],
  "properties": {
    "repo": {"type": "string", "description": "The repository, as owner/name."},
    "baseBranch": {"type": "string", "description": "The branch the pull request targets."},
    "title": {"type": "string", "minLength": 1, "maxLength": 120},
    "body": {"type": "string", "maxLength": 8000, "description": "The pull request description: what changes and why."},
    "files": {
      "type": "array",
      "minItems": 1,
      "maxItems": %d,
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
}`, gitchange.MaxFilesCeiling))

// decodeParams decodes the parameters, refusing unknown fields.
func decodeParams(raw json.RawMessage) (Params, error) {
	var p Params

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&p); err != nil {
		return Params{}, fmt.Errorf("decode the parameters: %w", err)
	}

	return p, nil
}

// plan checks the parameters against the limits that need no network and
// returns the changes with the format of each. A limit it breaks wraps
// action.ErrLimit.
func (l *Limits) plan(p Params) ([]change, error) {
	if !l.allowsRepo(p.Repo) {
		return nil, fmt.Errorf("repo %q is not one of limits.repos (%s): %w", p.Repo, strings.Join(l.Repos, ", "), action.ErrLimit)
	}

	if p.BaseBranch != l.BaseBranch {
		return nil, fmt.Errorf("baseBranch %q is not the allowed base branch %q: %w", p.BaseBranch, l.BaseBranch, action.ErrLimit)
	}

	if err := checkText(p); err != nil {
		return nil, fmt.Errorf("check the title and body: %w", err)
	}

	if len(p.Files) == 0 {
		return nil, errors.New("files: give at least one file")
	}

	if len(p.Files) > l.MaxFiles {
		return nil, fmt.Errorf("%d files, the most one pull request may change is %d: %w", len(p.Files), l.MaxFiles, action.ErrLimit)
	}

	changes := make([]change, 0, len(p.Files))
	seen := make(map[string]bool, len(p.Files))

	for _, f := range p.Files {
		c, err := l.planFile(f)
		if err != nil {
			return nil, fmt.Errorf("check the files: %w", err)
		}

		key := strings.ToLower(c.path)
		if seen[key] {
			return nil, fmt.Errorf("path %q is given twice", c.path)
		}

		seen[key] = true

		changes = append(changes, c)
	}

	return changes, nil
}

// planFile checks one file against the limits.
func (l *Limits) planFile(f FileChange) (change, error) {
	if err := checkPath(f.Path); err != nil {
		return change{}, fmt.Errorf("path %q: %w", f.Path, err)
	}

	format, err := l.Paths.FormatOf(f.Path)
	if err != nil {
		return change{}, fmt.Errorf("path %q %w", f.Path, err)
	}

	if hasContent := f.Content != nil; hasContent == f.Delete {
		return change{}, fmt.Errorf("path %q: %w", f.Path, errBothOrNone)
	}

	if f.Content != nil && !validContent(*f.Content) {
		return change{}, fmt.Errorf("path %q: content must be valid UTF-8 text without NUL characters", f.Path)
	}

	return change{path: f.Path, format: format, content: f.Content}, nil
}

// checkText checks the title and the body.
func checkText(p Params) error {
	switch {
	case strings.TrimSpace(p.Title) == "":
		return errors.New("title: required")
	case len([]rune(p.Title)) > maxTitleChars:
		return fmt.Errorf("title: longer than %d characters", maxTitleChars)
	case len([]rune(p.Body)) > maxBodyChars:
		return fmt.Errorf("body: longer than %d characters", maxBodyChars)
	case !validContent(p.Title) || !validContent(p.Body):
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

// validContent reports whether s is text Git can store as it is and
// Postgres can keep: valid UTF-8 without NUL.
func validContent(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

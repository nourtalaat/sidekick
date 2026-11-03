package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// Limits of the line diff of one file.
const (
	// MaxDiffLines is the most lines FileDiff returns unless the whole file
	// is asked for.
	MaxDiffLines = 400
	// DiffContext is how many unchanged lines show on each side of a
	// change.
	DiffContext = 3
	// ReadTimeout is how long the host may take to give the base of a file.
	ReadTimeout = 30 * time.Second
)

var (
	// ErrNoSuchFile means the snapshot of the proposal has no file with the
	// path.
	ErrNoSuchFile = errors.New("the proposal does not change a file with this path")
	// ErrDiffUnavailable means the action that made the proposal is not in
	// the config any more, or cannot read files from its host.
	ErrDiffUnavailable = errors.New("the action cannot read files from its host")
)

// UnreadableError means the host did not give the base of a file.
type UnreadableError struct {
	Err error
}

// Error implements error.
func (e *UnreadableError) Error() string {
	return "read the base of the file: " + e.Err.Error()
}

// Unwrap returns the error of the action.
func (e *UnreadableError) Unwrap() error { return e.Err }

// FileDiff is the line diff of one file of a pull request or merge request
// proposal.
type FileDiff struct {
	Path string
	// Status is "changed", "added" or "deleted".
	Status string
	// Lines are the lines of the diff, with the text of each written for a
	// person to read, as action.DisplayText writes it.
	Lines []gitchange.Line
	// Truncated says more lines follow than the ones returned.
	Truncated bool
	// FinalNewline is gitchange.NewlineAdded or gitchange.NewlineRemoved
	// when the change touches the newline at the end of the file, and empty
	// otherwise.
	FinalNewline string
}

// FileDiff returns the line diff of the file at path in the proposal's pull
// request or merge request, from the base the host has to the content in the
// proposal's parameters. The base is read through the action when the person
// asks, by the blob SHA in the snapshot; nothing is stored. Unless full is set,
// it holds the changed lines with DiffContext lines around them, cut at
// MaxDiffLines; with full set it holds every line of the file. A proposal of
// any status answers. It returns ErrNotFound, ErrNoSuchFile,
// ErrDiffUnavailable, or an *UnreadableError when the host did not give the
// base.
func (s *Service) FileDiff(ctx context.Context, id uuid.UUID, path string, full bool) (FileDiff, error) {
	v, err := Get(ctx, s.opts.Pool, id)
	if err != nil {
		return FileDiff{}, fmt.Errorf("read the proposal: %w", err)
	}

	i := slices.IndexFunc(v.Files, func(f action.ProposalFile) bool { return f.Path == path })
	if i < 0 {
		return FileDiff{}, ErrNoSuchFile
	}

	file := v.Files[i]

	reader, ok := s.opts.Actions[v.Action].(action.FileReader)
	if !ok {
		return FileDiff{}, ErrDiffUnavailable
	}

	base, err := baseOf(ctx, reader, v, file)
	if err != nil {
		return FileDiff{}, fmt.Errorf("get the base of %q: %w", path, err)
	}

	updated, err := proposedContent(ctx, reader, v, file)
	if err != nil {
		return FileDiff{}, fmt.Errorf("read the new content of %q: %w", path, err)
	}

	diff, ok := gitchange.LineDiff(base, updated, gitchange.MaxChangedLinesCeiling)
	if !ok {
		return FileDiff{}, fmt.Errorf("the two versions of %q differ in more than %d lines", path, gitchange.MaxChangedLinesCeiling)
	}

	lines, truncated := visibleLines(diff, full)

	return FileDiff{Path: path, Status: file.Status, Lines: spelled(lines), Truncated: truncated, FinalNewline: diff.FinalNewline}, nil
}

// ReadsFiles reports whether the action with this name is configured and can
// read files from its host, so a person can open the diff of a file of one of
// its proposals. It makes no network call.
func (s *Service) ReadsFiles(name string) bool {
	_, ok := s.opts.Actions[name].(action.FileReader)

	return ok
}

// baseOf reads the text of the file on the host's base branch, as the snapshot
// names it by blob SHA. A new file has none.
func baseOf(ctx context.Context, reader action.FileReader, v View, file action.ProposalFile) (string, error) {
	if file.Status == gitchange.FileAdded {
		return "", nil
	}

	sides, err := gitchange.ReadSnapshot(*v.Snapshot)
	if err != nil {
		return "", fmt.Errorf("read the snapshot: %w", err)
	}

	blob := gitchange.BlobOf(sides.Before[file.Path])
	if blob == "" {
		return "", fmt.Errorf("the snapshot names no base blob for %q", file.Path)
	}

	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()

	text, err := reader.ReadBlob(ctx, v.Params, blob)
	if err != nil {
		return "", &UnreadableError{Err: err}
	}

	return text, nil
}

// proposedContent is the text the proposal gives the file: the content in the
// parameters, or nothing for a file that is deleted. A proposal whose
// parameters hold no files, such as a revert, names the new content only by
// the blob SHA in its snapshot, which is read through the action.
func proposedContent(ctx context.Context, reader action.FileReader, v View, file action.ProposalFile) (string, error) {
	if file.Status == gitchange.FileDeleted {
		return "", nil
	}

	var edit gitchange.Edit
	if err := json.Unmarshal(v.Params, &edit); err != nil {
		return "", fmt.Errorf("decode the parameters: %w", err)
	}

	if len(edit.Files) == 0 {
		return afterBlob(ctx, reader, v, file)
	}

	i := slices.IndexFunc(edit.Files, func(f gitchange.FileChange) bool { return f.Path == file.Path })
	if i < 0 || edit.Files[i].Content == nil {
		return "", errors.New("the parameters hold no content for it")
	}

	return *edit.Files[i].Content, nil
}

// afterBlob reads the text of the file as the snapshot says the proposal
// leaves it.
func afterBlob(ctx context.Context, reader action.FileReader, v View, file action.ProposalFile) (string, error) {
	sides, err := gitchange.ReadSnapshot(*v.Snapshot)
	if err != nil {
		return "", fmt.Errorf("read the snapshot: %w", err)
	}

	blob := sides.After[file.Path].Blob
	if blob == "" {
		return "", fmt.Errorf("the snapshot names no new blob for %q", file.Path)
	}

	ctx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()

	text, err := reader.ReadBlob(ctx, v.Params, blob)
	if err != nil {
		return "", &UnreadableError{Err: err}
	}

	return text, nil
}

// visibleLines returns the lines to answer with, and whether more follow.
func visibleLines(d gitchange.Diff, full bool) ([]gitchange.Line, bool) {
	if full {
		return d.Lines, false
	}

	lines := d.Hunks(DiffContext)
	if len(lines) <= MaxDiffLines {
		return lines, false
	}

	return lines[:MaxDiffLines], true
}

// spelled returns the lines with the text of each written for a person to
// read.
func spelled(lines []gitchange.Line) []gitchange.Line {
	out := make([]gitchange.Line, len(lines))

	for i, l := range lines {
		l.Text = action.DisplayText(l.Text)
		out[i] = l
	}

	return out
}

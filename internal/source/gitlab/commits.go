package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of get_commit and compare.
const (
	// maxMessageLen bounds the message of one commit.
	maxMessageLen = 16 << 10
	// maxPatchBytes bounds the patch text of one commit, over all its
	// files. Patches past it are cut or left out.
	maxPatchBytes = 64 << 10
	// maxChangedFiles is the most changed files get_commit and compare list.
	maxChangedFiles = 300
)

// Texts of files whose diff GitLab does not send.
const (
	omittedTooLarge  = "diff too large for GitLab to send"
	omittedCollapsed = "diff collapsed by GitLab"
)

// apiCommit is a commit as GitLab's commit endpoints answer.
type apiCommit struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Message       string    `json:"message"`
	AuthorName    string    `json:"author_name"`
	CommitterName string    `json:"committer_name"`
	CreatedAt     time.Time `json:"created_at"`
	CommittedDate time.Time `json:"committed_date"`
	ParentIDs     []string  `json:"parent_ids"`
	WebURL        string    `json:"web_url"`
}

// commitItem is one commit in a list.
type commitItem struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	URL     string    `json:"url,omitempty"`
}

// apiCommitDetail is a commit as GitLab's single commit endpoint answers.
type apiCommitDetail struct {
	apiCommit

	Stats struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"stats"`
}

// apiDiff is the diff of one file as GitLab answers it. GitLab sends no line
// counts, so they are counted from Diff.
type apiDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"too_large"`
}

// apiCompare is the answer of GitLab's compare endpoint.
type apiCompare struct {
	Commits        []apiCommit `json:"commits"`
	Diffs          []apiDiff   `json:"diffs"`
	CompareTimeout bool        `json:"compare_timeout"`
	WebURL         string      `json:"web_url"`
}

// commitDetail is the result of get_commit.
type commitDetail struct {
	Project   string       `json:"project"`
	SHA       string       `json:"sha"`
	Message   string       `json:"message"`
	Author    string       `json:"author,omitempty"`
	Committer string       `json:"committer,omitempty"`
	Date      time.Time    `json:"date,omitzero"`
	Parents   []string     `json:"parents,omitempty"`
	Additions int          `json:"additions"`
	Deletions int          `json:"deletions"`
	Files     []fileChange `json:"files"`
	URL       string       `json:"url,omitempty"`
}

// fileChange is one file a commit or a comparison changed.
type fileChange struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previousFilename,omitempty"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch,omitempty"`
	// PatchCut is true when the patch was cut or left out to keep the
	// result small.
	PatchCut bool `json:"patchCut,omitempty"`
	// Omitted says why GitLab sent no diff, so the line counts are not
	// known and show as 0.
	Omitted string `json:"omitted,omitempty"`
}

// comparison is the result of compare.
type comparison struct {
	Project      string       `json:"project"`
	Base         string       `json:"base"`
	Head         string       `json:"head"`
	TotalCommits int          `json:"totalCommits"`
	Commits      []commitItem `json:"commits"`
	Files        []fileChange `json:"files"`
	// TimedOut is true when GitLab gave up part of the comparison, so the
	// files may be incomplete.
	TimedOut bool   `json:"timedOut,omitempty"`
	URL      string `json:"url,omitempty"`
}

// listCommits runs list_commits.
func (s *Source) listCommits(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since string `json:"since"`
		Until string `json:"until"`
		Path  string `json:"path"`
		Ref   string `json:"ref"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	since, err := parseTime("since", in.Since)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	until, err := parseTime("until", in.Until)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if err := checkRef("ref", in.Ref); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit)

	query := url.Values{}
	setTime(query, "since", since)
	setTime(query, "until", until)
	setIf(query, "path", in.Path)
	setIf(query, "ref_name", in.Ref)

	path := gitlabapi.ProjectPath(project, "repository", "commits")

	commits, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiCommit, int, error) {
		return gitlabapi.ListPage[apiCommit](ctx, s.client, path, query, page, min(s.pageSize, limit))
	}, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the commits of %s: %w", project, err)
	}

	items := make([]commitItem, 0, len(commits))
	evidence := make([]source.Evidence, 0, len(commits))

	for _, c := range commits {
		item := listedCommit(c)
		items = append(items, item)
		evidence = append(evidence, commitEvidence(project, item.SHA, item.URL, item.Date))
	}

	return structured(map[string]any{"project": project, "commits": items}, evidence, more, len(items) == 0)
}

// getCommit runs get_commit.
func (s *Source) getCommit(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Ref string `json:"ref"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.Ref == "" {
		return source.Result{}, &source.ToolError{Message: "give ref, the commit SHA"}
	}

	if err := checkRef("ref", in.Ref); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	base := gitlabapi.ProjectPath(project, "repository", "commits", url.PathEscape(in.Ref))

	var c apiCommitDetail

	if _, err := s.client.Get(ctx, base, nil, &c); err != nil {
		return source.Result{}, fmt.Errorf("get commit %s of %s: %w", in.Ref, project, err)
	}

	diffs, more, err := collect(ctx, maxChangedFiles, func(ctx context.Context, page int) ([]apiDiff, int, error) {
		return gitlabapi.ListPage[apiDiff](ctx, s.client, base+"/diff", nil, page, s.pageSize)
	}, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("get the diff of commit %s of %s: %w", in.Ref, project, err)
	}

	files, cut := changedFiles(diffs, true)
	detail := commitDetail{
		Project:   project,
		SHA:       c.ID,
		Message:   firstLines(c.Message, maxMessageLen),
		Author:    c.AuthorName,
		Committer: c.CommitterName,
		Date:      c.date(),
		Parents:   c.ParentIDs,
		Additions: c.Stats.Additions,
		Deletions: c.Stats.Deletions,
		Files:     files,
		URL:       safeURL(c.WebURL),
	}

	evidence := []source.Evidence{commitEvidence(project, detail.SHA, detail.URL, detail.Date)}

	return structured(detail, evidence, cut || more, false)
}

// compare runs compare. It compares from the merge base of the two refs, so
// the commits are those of head that base lacks.
func (s *Source) compare(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Base  string `json:"base"`
		Head  string `json:"head"`
		Limit int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.Base == "" || in.Head == "" {
		return source.Result{}, &source.ToolError{Message: "give both base and head"}
	}

	if err := errors.Join(checkRef("base", in.Base), checkRef("head", in.Head)); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	var c apiCompare

	query := url.Values{"from": {in.Base}, "to": {in.Head}}
	if _, err := s.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "compare"), query, &c); err != nil {
		return source.Result{}, fmt.Errorf("compare %s...%s of %s: %w", in.Base, in.Head, project, err)
	}

	commits := newestFirst(c.Commits)
	limit := limitOf(in.Limit)

	files, cut := changedFiles(c.Diffs, false)
	truncated := cut || len(commits) > limit || len(files) > maxChangedFiles || c.CompareTimeout
	files = files[:min(len(files), maxChangedFiles)]

	out := comparison{
		Project:      project,
		Base:         in.Base,
		Head:         in.Head,
		TotalCommits: len(commits),
		Commits:      make([]commitItem, 0, min(len(commits), limit)),
		Files:        files,
		TimedOut:     c.CompareTimeout,
		URL:          safeURL(c.WebURL),
	}

	for _, rc := range commits[:min(len(commits), limit)] {
		out.Commits = append(out.Commits, listedCommit(rc))
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceCommit,
		Title: fmt.Sprintf("%s %s...%s", project, in.Base, in.Head),
		URL:   out.URL,
	}}

	return structured(out, evidence, truncated, out.TotalCommits == 0 && len(files) == 0)
}

// listedCommit returns a commit as a list shows it.
func listedCommit(c apiCommit) commitItem {
	message := c.Message
	if message == "" {
		message = c.Title
	}

	return commitItem{
		SHA:     c.ID,
		Message: firstLines(message, maxListMessageLen),
		Author:  c.AuthorName,
		Date:    c.date(),
		URL:     safeURL(c.WebURL),
	}
}

// date returns when the commit was made, in UTC. Some answers carry only
// created_at, which GitLab documents as always equal to committed_date.
func (c apiCommit) date() time.Time {
	if !c.CommittedDate.IsZero() {
		return utc(c.CommittedDate)
	}

	return utc(c.CreatedAt)
}

// newestFirst returns the commits sorted by date, newest first. The order
// GitLab answers a comparison in is not documented, so it is not relied on.
func newestFirst(commits []apiCommit) []apiCommit {
	sorted := slices.Clone(commits)
	slices.SortStableFunc(sorted, func(a, b apiCommit) int { return b.date().Compare(a.date()) })

	return sorted
}

// changedFiles returns the changed files and whether any patch was cut or
// any diff left out. With withPatches, the files keep at most maxPatchBytes
// of patch text in all; without it, they keep none.
func changedFiles(diffs []apiDiff, withPatches bool) ([]fileChange, bool) {
	out := make([]fileChange, 0, len(diffs))
	budget, cut := maxPatchBytes, false

	for _, d := range diffs {
		change := fileChange{Filename: d.NewPath, Status: d.status()}
		if d.RenamedFile {
			change.PreviousFilename = d.OldPath
		}

		switch {
		case d.TooLarge:
			change.Omitted, cut = omittedTooLarge, true
		case d.Collapsed:
			change.Omitted, cut = omittedCollapsed, true
		default:
			change.Additions, change.Deletions = countLines(d.Diff)
		}

		if withPatches && d.Diff != "" {
			change.Patch = source.CutText(d.Diff, budget)
			change.PatchCut = len(change.Patch) < len(d.Diff)
			budget -= len(change.Patch)
			cut = cut || change.PatchCut
		}

		out = append(out, change)
	}

	return out, cut
}

// status returns the kind of change the diff shows.
func (d apiDiff) status() string {
	switch {
	case d.NewFile:
		return "added"
	case d.DeletedFile:
		return "removed"
	case d.RenamedFile:
		return "renamed"
	default:
		return "modified"
	}
}

// countLines counts the added and removed lines of a diff. GitLab's diff
// text has no file header lines, only hunks, so every line that starts with
// + or - is a change.
func countLines(diff string) (added, removed int) {
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}

	return added, removed
}

// setTime sets key to t in RFC 3339 form when t is not the zero time.
func setTime(q url.Values, key string, t time.Time) {
	if !t.IsZero() {
		q.Set(key, t.UTC().Format(time.RFC3339))
	}
}

// setIf sets key to value when value is not empty.
func setIf(q url.Values, key, value string) {
	if value != "" {
		q.Set(key, value)
	}
}

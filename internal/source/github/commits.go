package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of get_commit and compare.
const (
	// maxPatchBytes bounds the patch text of one commit, over all its
	// files. Patches past it are cut or left out.
	maxPatchBytes = 64 << 10
	// maxCompareFiles is the most changed files compare lists, which is
	// also the most GitHub sends.
	maxCompareFiles = 300
)

// commitItem is one commit in a list.
type commitItem struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	URL     string    `json:"url,omitempty"`
}

// commitDetail is the result of get_commit.
type commitDetail struct {
	Repo      string       `json:"repo"`
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
}

// comparison is the result of compare.
type comparison struct {
	Repo         string       `json:"repo"`
	Base         string       `json:"base"`
	Head         string       `json:"head"`
	Status       string       `json:"status"`
	AheadBy      int          `json:"aheadBy"`
	BehindBy     int          `json:"behindBy"`
	TotalCommits int          `json:"totalCommits"`
	Commits      []commitItem `json:"commits"`
	Files        []fileChange `json:"files"`
	URL          string       `json:"url,omitempty"`
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

	r, err := s.resolve(in.target)
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

	commits, more, err := collect(ctx, min(s.pageSize, limit), limit,
		func(ctx context.Context, page gh.ListOptions) ([]*gh.RepositoryCommit, *gh.Response, error) {
			return s.client.Repositories.ListCommits(ctx, r.owner, r.name, &gh.CommitsListOptions{
				SHA: in.Ref, Path: in.Path, Since: since, Until: until, ListOptions: page,
			})
		}, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the commits of %s: %w", r, err)
	}

	items := make([]commitItem, 0, len(commits))
	evidence := make([]source.Evidence, 0, len(commits))

	for _, c := range commits {
		item := listedCommit(c)
		items = append(items, item)
		evidence = append(evidence, commitEvidence(r, item.SHA, item.URL, item.Date))
	}

	return structured(map[string]any{"repo": r.String(), "commits": items}, evidence, more, len(items) == 0)
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

	r, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.Ref == "" {
		return source.Result{}, &source.ToolError{Message: "give ref, the commit SHA"}
	}

	if err := checkRef("ref", in.Ref); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	c, _, err := s.client.Repositories.GetCommit(ctx, r.owner, r.name, in.Ref, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("get commit %s of %s: %w", in.Ref, r, err)
	}

	files, cut := changedFiles(c.Files, true)
	detail := commitDetail{
		Repo:      r.String(),
		SHA:       c.GetSHA(),
		Message:   firstLines(c.GetCommit().GetMessage(), maxMessageLen),
		Author:    authorOf(c),
		Committer: c.GetCommitter().GetLogin(),
		Date:      timeOf(c.GetCommit().GetCommitter().Date),
		Additions: c.GetStats().GetAdditions(),
		Deletions: c.GetStats().GetDeletions(),
		Files:     files,
		URL:       c.GetHTMLURL(),
	}

	for _, p := range c.Parents {
		detail.Parents = append(detail.Parents, p.GetSHA())
	}

	evidence := []source.Evidence{commitEvidence(r, detail.SHA, detail.URL, detail.Date)}

	return structured(detail, evidence, cut, false)
}

// compare runs compare.
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

	r, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.Base == "" || in.Head == "" {
		return source.Result{}, &source.ToolError{Message: "give both base and head"}
	}

	if err := errors.Join(checkRef("base", in.Base), checkRef("head", in.Head)); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	c, _, err := s.client.Repositories.CompareCommits(ctx, r.owner, r.name, in.Base, in.Head, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("compare %s...%s of %s: %w", in.Base, in.Head, r, err)
	}

	limit := limitOf(in.Limit)
	commits := c.Commits[max(len(c.Commits)-limit, 0):]

	files, _ := changedFiles(c.Files, false)
	truncated := len(commits) < c.GetTotalCommits() || len(files) > maxCompareFiles
	files = files[:min(len(files), maxCompareFiles)]

	out := comparison{
		Repo:         r.String(),
		Base:         in.Base,
		Head:         in.Head,
		Status:       c.GetStatus(),
		AheadBy:      c.GetAheadBy(),
		BehindBy:     c.GetBehindBy(),
		TotalCommits: c.GetTotalCommits(),
		Commits:      make([]commitItem, 0, len(commits)),
		Files:        files,
		URL:          c.GetHTMLURL(),
	}

	for _, rc := range commits {
		out.Commits = append(out.Commits, listedCommit(rc))
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceCommit,
		Title: fmt.Sprintf("%s %s...%s", r, in.Base, in.Head),
		URL:   out.URL,
	}}

	return structured(out, evidence, truncated, out.TotalCommits == 0 && len(files) == 0)
}

// listedCommit returns a commit as a list shows it.
func listedCommit(c *gh.RepositoryCommit) commitItem {
	return commitItem{
		SHA:     c.GetSHA(),
		Message: firstLines(c.GetCommit().GetMessage(), maxListMessageLen),
		Author:  authorOf(c),
		Date:    timeOf(c.GetCommit().GetCommitter().Date),
		URL:     c.GetHTMLURL(),
	}
}

// authorOf returns the GitHub login of a commit's author, or the name in
// the commit when the author has no GitHub account. Email addresses are
// left out.
func authorOf(c *gh.RepositoryCommit) string {
	if login := c.GetAuthor().GetLogin(); login != "" {
		return login
	}

	return c.GetCommit().GetAuthor().GetName()
}

// changedFiles returns the changed files and whether any patch was cut or
// left out. With withPatches, the files keep at most maxPatchBytes of patch
// text in all; without it, they keep none.
func changedFiles(files []*gh.CommitFile, withPatches bool) ([]fileChange, bool) {
	out := make([]fileChange, 0, len(files))
	budget, cut := maxPatchBytes, false

	for _, f := range files {
		change := fileChange{
			Filename:         f.GetFilename(),
			PreviousFilename: f.GetPreviousFilename(),
			Status:           f.GetStatus(),
			Additions:        f.GetAdditions(),
			Deletions:        f.GetDeletions(),
		}

		if patch := f.GetPatch(); withPatches && patch != "" {
			change.Patch = source.CutText(patch, budget)
			change.PatchCut = len(change.Patch) < len(patch)
			budget -= len(change.Patch)
			cut = cut || change.PatchCut
		}

		out = append(out, change)
	}

	return out, cut
}

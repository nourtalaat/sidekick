package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
)

// mergeRequestPageSize is the most merge requests one page of list_merge_requests
// asks for. Their descriptions can be long, so pages are kept small.
const mergeRequestPageSize = 30

// apiMergeRequest is a merge request as GitLab's list endpoint answers.
type apiMergeRequest struct {
	IID    int    `json:"iid"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	SourceBranch    string    `json:"source_branch"`
	TargetBranch    string    `json:"target_branch"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	MergedAt        time.Time `json:"merged_at"`
	ClosedAt        time.Time `json:"closed_at"`
	MergeCommitSHA  string    `json:"merge_commit_sha"`
	SquashCommitSHA string    `json:"squash_commit_sha"`
	WebURL          string    `json:"web_url"`
}

// mergeRequestItem is one merge request in a list.
type mergeRequestItem struct {
	IID   int    `json:"iid"`
	Title string `json:"title"`
	// State is opened, closed, locked or merged.
	State        string    `json:"state"`
	Draft        bool      `json:"draft,omitempty"`
	Author       string    `json:"author,omitempty"`
	Base         string    `json:"base"`
	Head         string    `json:"head"`
	Created      time.Time `json:"created,omitzero"`
	Merged       time.Time `json:"merged,omitzero"`
	Closed       time.Time `json:"closed,omitzero"`
	MergeCommit  string    `json:"mergeCommit,omitempty"`
	SquashCommit string    `json:"squashCommit,omitempty"`
	URL          string    `json:"url,omitempty"`
}

// listMergeRequests runs list_merge_requests. It asks GitLab for the merge
// requests updated since the time, most recently updated first, and keeps
// those opened or merged since then: a merge request that only got a comment
// is not news. The list is sorted, so it stops at the first one last updated
// before since.
func (s *Source) listMergeRequests(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since string `json:"since"`
		Base  string `json:"base"`
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

	if err := checkRef("base", in.Base); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit)

	query := url.Values{"state": {"all"}, "order_by": {"updated_at"}, "sort": {"desc"}}
	setTime(query, "updated_after", since)
	setIf(query, "target_branch", in.Base)

	path := gitlabapi.ProjectPath(project, "merge_requests")

	requests, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiMergeRequest, int, error) {
		return gitlabapi.ListPage[apiMergeRequest](ctx, s.client, path, query, page, min(s.pageSize, limit, mergeRequestPageSize))
	}, func(m apiMergeRequest) (bool, bool) {
		if m.UpdatedAt.Before(since) {
			return false, true
		}

		return !m.CreatedAt.Before(since) || !m.MergedAt.Before(since), false
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the merge requests of %s: %w", project, err)
	}

	items := make([]mergeRequestItem, 0, len(requests))
	evidence := make([]source.Evidence, 0, len(requests))

	for _, m := range requests {
		item := mergeRequestItem{
			IID:          m.IID,
			Title:        source.CutText(m.Title, maxTitleLen),
			State:        m.State,
			Draft:        m.Draft,
			Author:       m.Author.Username,
			Base:         m.TargetBranch,
			Head:         m.SourceBranch,
			Created:      utc(m.CreatedAt),
			Merged:       utc(m.MergedAt),
			Closed:       utc(m.ClosedAt),
			MergeCommit:  m.MergeCommitSHA,
			SquashCommit: m.SquashCommitSHA,
			URL:          safeURL(m.WebURL),
		}

		items = append(items, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceCommit,
			Title: fmt.Sprintf("%s!%d", project, item.IID),
			URL:   item.URL,
			At:    latest(item.Created, item.Merged),
		})
	}

	return structured(map[string]any{"project": project, "mergeRequests": items}, evidence, more, len(items) == 0)
}

// latest returns the later of two times.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}

	return a
}

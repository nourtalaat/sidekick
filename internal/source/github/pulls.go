package github

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// pullItem is one pull request in a list.
type pullItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// State is open, closed or merged.
	State       string    `json:"state"`
	Draft       bool      `json:"draft,omitempty"`
	Author      string    `json:"author,omitempty"`
	Base        string    `json:"base"`
	Head        string    `json:"head"`
	Created     time.Time `json:"created,omitzero"`
	Merged      time.Time `json:"merged,omitzero"`
	Closed      time.Time `json:"closed,omitzero"`
	MergeCommit string    `json:"mergeCommit,omitempty"`
	URL         string    `json:"url,omitempty"`
}

// listPullRequests runs list_pull_requests. GitHub cannot filter pull
// requests by time, so it reads them most recently updated first and stops
// at the first one last updated before since: nothing after it can have
// been opened or merged since then.
func (s *Source) listPullRequests(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since string `json:"since"`
		Base  string `json:"base"`
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

	if err := checkRef("base", in.Base); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	pulls, more, err := collect(ctx, s.pageSize, limitOf(in.Limit),
		func(ctx context.Context, page gh.ListOptions) ([]*gh.PullRequest, *gh.Response, error) {
			return s.client.PullRequests.List(ctx, r.owner, r.name, &gh.PullRequestListOptions{
				State: "all", Base: in.Base, Sort: "updated", Direction: "desc", ListOptions: page,
			})
		},
		func(p *gh.PullRequest) (bool, bool) {
			if timeOf(p.UpdatedAt).Before(since) {
				return false, true
			}

			return !timeOf(p.CreatedAt).Before(since) || !timeOf(p.MergedAt).Before(since), false
		})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the pull requests of %s: %w", r, err)
	}

	items := make([]pullItem, 0, len(pulls))
	evidence := make([]source.Evidence, 0, len(pulls))

	for _, p := range pulls {
		item := pullItem{
			Number:      p.GetNumber(),
			Title:       source.CutText(p.GetTitle(), maxTitleLen),
			State:       p.GetState(),
			Draft:       p.GetDraft(),
			Author:      p.GetUser().GetLogin(),
			Base:        p.GetBase().GetRef(),
			Head:        p.GetHead().GetRef(),
			Created:     timeOf(p.CreatedAt),
			Merged:      timeOf(p.MergedAt),
			Closed:      timeOf(p.ClosedAt),
			MergeCommit: p.GetMergeCommitSHA(),
			URL:         p.GetHTMLURL(),
		}

		if !item.Merged.IsZero() {
			item.State = "merged"
		}

		items = append(items, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceCommit,
			Title: fmt.Sprintf("%s#%d", r, item.Number),
			URL:   item.URL,
			At:    latest(item.Created, item.Merged),
		})
	}

	return structured(map[string]any{"repo": r.String(), "pullRequests": items}, evidence, more, len(items) == 0)
}

// latest returns the later of two times.
func latest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}

	return a
}

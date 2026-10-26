package github

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// releaseItem is one release in a list.
type releaseItem struct {
	Tag        string    `json:"tag"`
	Name       string    `json:"name,omitempty"`
	Draft      bool      `json:"draft,omitempty"`
	Prerelease bool      `json:"prerelease,omitempty"`
	Target     string    `json:"target,omitempty"`
	Author     string    `json:"author,omitempty"`
	Created    time.Time `json:"created,omitzero"`
	Published  time.Time `json:"published,omitzero"`
	URL        string    `json:"url,omitempty"`
}

// tagItem is one tag in a list.
type tagItem struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

// releases is the result of list_releases.
type releases struct {
	Repo     string        `json:"repo"`
	Releases []releaseItem `json:"releases"`
	Tags     []tagItem     `json:"tags,omitempty"`
}

// listReleases runs list_releases. Releases come newest first, so the
// first one made before since ends the list.
func (s *Source) listReleases(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since       string `json:"since"`
		IncludeTags bool   `json:"includeTags"`
		Limit       int    `json:"limit"`
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

	limit := limitOf(in.Limit)

	var keep filter[*gh.RepositoryRelease]
	if !since.IsZero() {
		keep = func(rel *gh.RepositoryRelease) (bool, bool) {
			return true, timeOf(&rel.CreatedAt).Before(since)
		}
	}

	list, more, err := collect(ctx, min(s.pageSize, limit), limit,
		func(ctx context.Context, page gh.ListOptions) ([]*gh.RepositoryRelease, *gh.Response, error) {
			return s.client.Repositories.ListReleases(ctx, r.owner, r.name, &page)
		}, keep)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the releases of %s: %w", r, err)
	}

	out := releases{Repo: r.String(), Releases: make([]releaseItem, 0, len(list))}
	evidence := make([]source.Evidence, 0, len(list))

	for _, rel := range list {
		item := releaseItem{
			Tag:        rel.GetTagName(),
			Name:       source.CutText(rel.GetName(), maxTitleLen),
			Draft:      rel.GetDraft(),
			Prerelease: rel.GetPrerelease(),
			Target:     rel.GetTargetCommitish(),
			Author:     rel.GetAuthor().GetLogin(),
			Created:    timeOf(&rel.CreatedAt),
			Published:  timeOf(rel.PublishedAt),
			URL:        rel.GetHTMLURL(),
		}

		out.Releases = append(out.Releases, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("%s release %s", r, item.Tag),
			URL:   item.URL,
			At:    latest(item.Created, item.Published),
		})
	}

	if in.IncludeTags {
		tags, _, err := s.client.Repositories.ListTags(ctx, r.owner, r.name, &gh.ListOptions{PerPage: limit})
		if err != nil {
			return source.Result{}, fmt.Errorf("list the tags of %s: %w", r, err)
		}

		for _, tag := range tags {
			out.Tags = append(out.Tags, tagItem{Name: tag.GetName(), SHA: tag.GetCommit().GetSHA()})
		}
	}

	return structured(out, evidence, more, len(out.Releases) == 0 && len(out.Tags) == 0)
}

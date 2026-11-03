package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
)

// apiRelease is a release as GitLab's releases endpoint answers.
type apiRelease struct {
	TagName         string    `json:"tag_name"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"created_at"`
	ReleasedAt      time.Time `json:"released_at"`
	UpcomingRelease bool      `json:"upcoming_release"`
	Author          struct {
		Username string `json:"username"`
	} `json:"author"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
	Links struct {
		Self string `json:"self"`
	} `json:"_links"`
}

// apiTag is a tag as GitLab's tags endpoint answers.
type apiTag struct {
	Name      string    `json:"name"`
	Target    string    `json:"target"`
	CreatedAt time.Time `json:"created_at"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// releaseItem is one release in a list.
type releaseItem struct {
	Tag      string    `json:"tag"`
	Name     string    `json:"name,omitempty"`
	Upcoming bool      `json:"upcoming,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	Author   string    `json:"author,omitempty"`
	Created  time.Time `json:"created,omitzero"`
	Released time.Time `json:"released,omitzero"`
	URL      string    `json:"url,omitempty"`
}

// tagItem is one tag in a list.
type tagItem struct {
	Name    string    `json:"name"`
	SHA     string    `json:"sha"`
	Created time.Time `json:"created,omitzero"`
}

// releases is the result of list_releases.
type releases struct {
	Project  string        `json:"project"`
	Releases []releaseItem `json:"releases"`
	Tags     []tagItem     `json:"tags,omitempty"`
}

// listReleases runs list_releases. GitLab sorts releases by the time they
// were released, newest first, so the first one released before since ends
// the list.
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

	project, err := s.resolve(in.target)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	since, err := parseTime("since", in.Since)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	limit := limitOf(in.Limit)

	var keep filter[apiRelease]
	if !since.IsZero() {
		keep = func(r apiRelease) (bool, bool) {
			return true, r.ReleasedAt.Before(since)
		}
	}

	path := gitlabapi.ProjectPath(project, "releases")

	list, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiRelease, int, error) {
		return gitlabapi.ListPage[apiRelease](ctx, s.client, path, nil, page, min(s.pageSize, limit))
	}, keep)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the releases of %s: %w", project, err)
	}

	out := releases{Project: project, Releases: make([]releaseItem, 0, len(list))}
	evidence := make([]source.Evidence, 0, len(list))

	for _, r := range list {
		item := releaseItem{
			Tag:      r.TagName,
			Name:     source.CutText(r.Name, maxTitleLen),
			Upcoming: r.UpcomingRelease,
			Commit:   r.Commit.ID,
			Author:   r.Author.Username,
			Created:  utc(r.CreatedAt),
			Released: utc(r.ReleasedAt),
			URL:      safeURL(r.Links.Self),
		}

		out.Releases = append(out.Releases, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("%s release %s", project, item.Tag),
			URL:   item.URL,
			At:    latest(item.Created, item.Released),
		})
	}

	if in.IncludeTags {
		tags, err := s.listTags(ctx, project, limit)
		if err != nil {
			return source.Result{}, fmt.Errorf("list the tags of %s: %w", project, err)
		}

		out.Tags = tags
	}

	return structured(out, evidence, more, len(out.Releases) == 0 && len(out.Tags) == 0)
}

// listTags reads the newest tags, one page of at most limit.
func (s *Source) listTags(ctx context.Context, project string, limit int) ([]tagItem, error) {
	var tags []apiTag

	query := url.Values{"per_page": {strconv.Itoa(limit)}}
	if _, err := s.client.Get(ctx, gitlabapi.ProjectPath(project, "repository", "tags"), query, &tags); err != nil {
		return nil, fmt.Errorf("get the tags: %w", err)
	}

	items := make([]tagItem, 0, len(tags))

	for _, tag := range tags {
		sha := tag.Commit.ID
		if sha == "" {
			sha = tag.Target
		}

		items = append(items, tagItem{Name: tag.Name, SHA: sha, Created: utc(tag.CreatedAt)})
	}

	return items, nil
}

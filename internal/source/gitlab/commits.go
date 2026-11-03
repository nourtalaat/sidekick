package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
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

	path := projectPath(project, "repository", "commits")

	commits, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiCommit, int, error) {
		return listPage[apiCommit](ctx, s.client, path, query, page, min(s.pageSize, limit))
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

// listedCommit returns a commit as a list shows it.
func listedCommit(c apiCommit) commitItem {
	return commitItem{
		SHA:     c.ID,
		Message: firstLines(c.Message, maxListMessageLen),
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

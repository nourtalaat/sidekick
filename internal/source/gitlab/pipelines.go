package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// apiPipeline is a pipeline as GitLab's project pipelines endpoint answers.
type apiPipeline struct {
	ID        int64     `json:"id"`
	IID       int       `json:"iid"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Source    string    `json:"source"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

// pipelineItem is one pipeline in a list.
type pipelineItem struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name,omitempty"`
	Status  string    `json:"status"`
	Source  string    `json:"source,omitempty"`
	Ref     string    `json:"ref,omitempty"`
	SHA     string    `json:"sha"`
	Created time.Time `json:"created,omitzero"`
	Updated time.Time `json:"updated,omitzero"`
	URL     string    `json:"url,omitempty"`
}

// listPipelines runs list_pipelines. Pipeline IDs grow with time, so the list
// sorted by ID, newest first, ends at the first pipeline created before
// since.
func (s *Source) listPipelines(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since  string `json:"since"`
		Ref    string `json:"ref"`
		Status string `json:"status"`
		Source string `json:"source"`
		Limit  int    `json:"limit"`
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

	if err := checkRef("ref", in.Ref); err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	if in.Source != "" && !wordPattern.MatchString(in.Source) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("source: %q is not a pipeline source such as push or schedule", in.Source)}
	}

	limit := limitOf(in.Limit)

	query := url.Values{"order_by": {"id"}, "sort": {"desc"}}
	setTime(query, "created_after", since)
	setIf(query, "ref", in.Ref)
	setIf(query, "status", in.Status)
	setIf(query, "source", in.Source)

	var keep filter[apiPipeline]
	if !since.IsZero() {
		keep = func(p apiPipeline) (bool, bool) {
			return true, p.CreatedAt.Before(since)
		}
	}

	path := projectPath(project, "pipelines")

	pipelines, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiPipeline, int, error) {
		return listPage[apiPipeline](ctx, s.client, path, query, page, min(s.pageSize, limit))
	}, keep)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the pipelines of %s: %w", project, err)
	}

	items := make([]pipelineItem, 0, len(pipelines))
	evidence := make([]source.Evidence, 0, len(pipelines))

	for _, p := range pipelines {
		item := pipelineItem{
			ID:      p.ID,
			Name:    source.CutText(p.Name, maxTitleLen),
			Status:  p.Status,
			Source:  p.Source,
			Ref:     p.Ref,
			SHA:     p.SHA,
			Created: utc(p.CreatedAt),
			Updated: utc(p.UpdatedAt),
			URL:     safeURL(p.WebURL),
		}

		items = append(items, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("%s pipeline #%d", project, item.ID),
			URL:   item.URL,
			At:    item.Created,
		})
	}

	return structured(map[string]any{"project": project, "pipelines": items}, evidence, more, len(items) == 0)
}

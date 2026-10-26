package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// runItem is one workflow run in a list.
type runItem struct {
	ID         int64     `json:"id"`
	Workflow   string    `json:"workflow"`
	Title      string    `json:"title,omitempty"`
	Number     int       `json:"number"`
	Attempt    int       `json:"attempt,omitempty"`
	Event      string    `json:"event"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	SHA        string    `json:"sha"`
	Actor      string    `json:"actor,omitempty"`
	Created    time.Time `json:"created,omitzero"`
	Started    time.Time `json:"started,omitzero"`
	Updated    time.Time `json:"updated,omitzero"`
	URL        string    `json:"url,omitempty"`
}

// listWorkflowRuns runs list_workflow_runs.
func (s *Source) listWorkflowRuns(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since    string `json:"since"`
		Workflow string `json:"workflow"`
		Branch   string `json:"branch"`
		Event    string `json:"event"`
		Status   string `json:"status"`
		Limit    int    `json:"limit"`
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

	if in.Workflow != "" && !workflowPattern.MatchString(in.Workflow) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("workflow: %q is not a workflow file name or ID", in.Workflow)}
	}

	opts := gh.ListWorkflowRunsOptions{Branch: in.Branch, Event: in.Event, Status: in.Status}
	if !since.IsZero() {
		opts.Created = ">=" + since.UTC().Format(time.RFC3339)
	}

	limit := limitOf(in.Limit)

	runs, more, err := collect(ctx, min(s.pageSize, limit), limit,
		func(ctx context.Context, page gh.ListOptions) ([]*gh.WorkflowRun, *gh.Response, error) {
			opts.ListOptions = page

			return s.workflowRuns(ctx, r, in.Workflow, &opts)
		}, nil)
	if err != nil {
		return source.Result{}, fmt.Errorf("list the workflow runs of %s: %w", r, err)
	}

	items := make([]runItem, 0, len(runs))
	evidence := make([]source.Evidence, 0, len(runs))

	for _, run := range runs {
		item := runItem{
			ID:         run.GetID(),
			Workflow:   source.CutText(run.GetName(), maxTitleLen),
			Title:      source.CutText(run.GetDisplayTitle(), maxTitleLen),
			Number:     run.GetRunNumber(),
			Attempt:    run.GetRunAttempt(),
			Event:      run.GetEvent(),
			Status:     run.GetStatus(),
			Conclusion: run.GetConclusion(),
			Branch:     run.GetHeadBranch(),
			SHA:        run.GetHeadSHA(),
			Actor:      run.GetActor().GetLogin(),
			Created:    timeOf(run.CreatedAt),
			Started:    timeOf(run.RunStartedAt),
			Updated:    timeOf(run.UpdatedAt),
			URL:        run.GetHTMLURL(),
		}

		items = append(items, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("%s %s #%d", r, item.Workflow, item.Number),
			URL:   item.URL,
			At:    item.Created,
		})
	}

	return structured(map[string]any{"repo": r.String(), "workflowRuns": items}, evidence, more, len(items) == 0)
}

// workflowRuns reads one page of runs: of every workflow, or of the one
// named by its numeric ID or its file name.
func (s *Source) workflowRuns(ctx context.Context, r repo, workflow string, opts *gh.ListWorkflowRunsOptions) ([]*gh.WorkflowRun, *gh.Response, error) {
	var (
		runs *gh.WorkflowRuns
		resp *gh.Response
		err  error
	)

	if id, parseErr := strconv.ParseInt(workflow, 10, 64); parseErr == nil {
		runs, resp, err = s.client.Actions.ListWorkflowRunsByID(ctx, r.owner, r.name, id, opts)
	} else if workflow != "" {
		runs, resp, err = s.client.Actions.ListWorkflowRunsByFileName(ctx, r.owner, r.name, workflow, opts)
	} else {
		runs, resp, err = s.client.Actions.ListRepositoryWorkflowRuns(ctx, r.owner, r.name, opts)
	}

	if err != nil {
		return nil, resp, fmt.Errorf("read workflow runs: %w", err)
	}

	return runs.WorkflowRuns, resp, nil
}

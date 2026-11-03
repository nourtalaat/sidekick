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

// apiDeployment is a deployment as GitLab's project deployments endpoint
// answers.
type apiDeployment struct {
	ID        int64     `json:"id"`
	IID       int       `json:"iid"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Username string `json:"username"`
	} `json:"user"`
	Environment struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"environment"`
	Deployable struct {
		FinishedAt time.Time `json:"finished_at"`
	} `json:"deployable"`
}

// deploymentItem is one deployment in a list.
type deploymentItem struct {
	ID          int64     `json:"id"`
	IID         int       `json:"iid"`
	Environment string    `json:"environment"`
	Status      string    `json:"status"`
	Ref         string    `json:"ref,omitempty"`
	SHA         string    `json:"sha"`
	Deployer    string    `json:"deployer,omitempty"`
	Created     time.Time `json:"created,omitzero"`
	Updated     time.Time `json:"updated,omitzero"`
	Finished    time.Time `json:"finished,omitzero"`
	URL         string    `json:"url,omitempty"`
}

// listDeployments runs list_deployments. GitLab can filter deployments by
// the time they were updated, so the call asks for those, newest created
// first, and keeps those created since the time: the list ends at the first
// deployment created before it.
func (s *Source) listDeployments(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		target

		Since       string `json:"since"`
		Environment string `json:"environment"`
		Status      string `json:"status"`
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

	if in.Environment != "" && !environmentPattern.MatchString(in.Environment) {
		return source.Result{}, &source.ToolError{Message: fmt.Sprintf("environment: %q is not an environment name", in.Environment)}
	}

	limit := limitOf(in.Limit)

	query := url.Values{"order_by": {"created_at"}, "sort": {"desc"}}
	setTime(query, "updated_after", since)
	setIf(query, "environment", in.Environment)
	setIf(query, "status", in.Status)

	path := gitlabapi.ProjectPath(project, "deployments")

	deployments, more, err := collect(ctx, limit, func(ctx context.Context, page int) ([]apiDeployment, int, error) {
		return gitlabapi.ListPage[apiDeployment](ctx, s.client, path, query, page, min(s.pageSize, limit))
	}, func(d apiDeployment) (bool, bool) {
		return true, d.CreatedAt.Before(since)
	})
	if err != nil {
		return source.Result{}, fmt.Errorf("list the deployments of %s: %w", project, err)
	}

	items := make([]deploymentItem, 0, len(deployments))
	evidence := make([]source.Evidence, 0, len(deployments))

	for _, d := range deployments {
		item := deploymentItem{
			ID:          d.ID,
			IID:         d.IID,
			Environment: source.CutText(d.Environment.Name, maxTitleLen),
			Status:      d.Status,
			Ref:         d.Ref,
			SHA:         d.SHA,
			Deployer:    d.User.Username,
			Created:     utc(d.CreatedAt),
			Updated:     utc(d.UpdatedAt),
			Finished:    utc(d.Deployable.FinishedAt),
			URL:         s.deploymentURL(project, d),
		}

		items = append(items, item)
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("%s deploy %s #%d", project, item.Environment, item.IID),
			URL:   item.URL,
			At:    item.Created,
		})
	}

	return structured(map[string]any{"project": project, "deployments": items}, evidence, more, len(items) == 0)
}

// deploymentURL returns the link to a deployment's page in GitLab, which the
// API does not give. It is empty when the deployment lacks the numbers the
// page address needs.
func (s *Source) deploymentURL(project string, d apiDeployment) string {
	if d.Environment.ID == 0 || d.IID == 0 {
		return ""
	}

	return s.cfg.baseURL() + "/" + project + "/-/environments/" + strconv.FormatInt(d.Environment.ID, 10) +
		"/deployments/" + strconv.Itoa(d.IID)
}

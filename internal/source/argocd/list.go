package argocd

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxEvidence bounds the application links one list_applications result
// gives.
const maxEvidence = 10

// Values of the sync and health status of an application, as Argo CD
// names them.
const (
	syncOutOfSync = "OutOfSync"
	healthHealthy = "Healthy"
	statusUnknown = "Unknown"
)

// projectPattern is the shape of a project name a call may filter by.
var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,251}[a-z0-9]$|^[a-z0-9]$`)

// listInput is the input of list_applications.
type listInput struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Limit   int    `json:"limit"`
}

// applicationList is the result of list_applications.
type applicationList struct {
	// Total is the number of applications that match, which may be more
	// than the list holds.
	Total        int                  `json:"total"`
	Applications []applicationSummary `json:"applications"`
}

// applicationSummary is one application in a list.
type applicationSummary struct {
	// Application is project/name, the form the other tools take.
	Application string      `json:"application"`
	Name        string      `json:"name"`
	Project     string      `json:"project"`
	Destination destination `json:"destination"`
	SyncStatus  string      `json:"syncStatus"`
	Health      string      `json:"health"`
	// AutoSync says whether Argo CD syncs the application by itself.
	AutoSync bool `json:"autoSync"`
	// DeployedRevision is the revision of the newest successful sync.
	DeployedRevision string `json:"deployedRevision,omitempty"`
	// ComparedRevision is the revision Argo CD last compared the
	// application with, such as the newest commit of the target branch. It
	// differs from DeployedRevision when the application is out of sync.
	ComparedRevision string    `json:"comparedRevision,omitempty"`
	LastSyncedAt     time.Time `json:"lastSyncedAt,omitzero"`
	URL              string    `json:"url"`
}

// destination is where an application deploys to.
type destination struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// listFilter is what a call narrows the list to.
type listFilter struct {
	project string
	name    string
	state   string
}

// listApplications runs list_applications.
func (s *Source) listApplications(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[listInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter, err := newListFilter(in)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	projects, err := s.projectsToRead(filter.project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	apps, err := s.client.ListApplications(ctx, argo.ListQuery{Projects: projects})
	if err != nil {
		return source.Result{}, fmt.Errorf("read the applications: %w", err)
	}

	matches := s.matching(apps, filter)
	list := applicationList{Total: len(matches)}

	var evidence []source.Evidence

	for _, app := range matches[:min(len(matches), limitOf(in.Limit, defaultApplications, maxApplications))] {
		summary := s.summarize(app)
		list.Applications = append(list.Applications, summary)

		if len(evidence) < maxEvidence {
			evidence = append(evidence, source.Evidence{
				Kind:  source.EvidenceResource,
				Title: "Argo CD application " + summary.Application,
				URL:   summary.URL,
				At:    summary.LastSyncedAt,
			})
		}
	}

	if list.Total == 0 {
		return source.Result{NoData: true, Text: "No Argo CD application that Sidekick may read" + filter.describe() + "."}, nil
	}

	data, err := json.Marshal(list)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: list.Total > len(list.Applications)}, nil
}

// projectsToRead returns the projects to ask Argo CD for: the one a call
// names, or the ones the config names when every glob names its project,
// or nil for every project. A project the config does not allow is a
// *source.ToolError.
func (s *Source) projectsToRead(project string) ([]string, error) {
	allowed := s.cfg.Applications.Projects()

	switch {
	case project == "":
		return allowed, nil
	case allowed == nil || slices.Contains(allowed, project):
		return []string{project}, nil
	default:
		return nil, &source.ToolError{Message: fmt.Sprintf("project %q is not one Sidekick may read; allowed projects: %s",
			project, strings.Join(allowed, ", "))}
	}
}

// matching returns the applications the config allows that match the
// filter, ordered by project and name.
func (s *Source) matching(apps []argo.Application, f listFilter) []*argo.Application {
	var out []*argo.Application

	for i := range apps {
		app := &apps[i]
		ref := app.Ref()

		if !s.cfg.Applications.Allows(ref) || !f.matches(app) {
			continue
		}

		out = append(out, app)
	}

	slices.SortFunc(out, func(a, b *argo.Application) int {
		return cmp.Or(cmp.Compare(a.Ref().Project, b.Ref().Project), cmp.Compare(a.Ref().Name, b.Ref().Name))
	})

	return out
}

// summarize returns the list entry of an application.
func (s *Source) summarize(app *argo.Application) applicationSummary {
	ref := app.Ref()

	summary := applicationSummary{
		Application:      ref.String(),
		Name:             ref.Name,
		Project:          ref.Project,
		Destination:      destinationOf(app.Spec.Destination),
		SyncStatus:       statusOf(app.Status.Sync.Status),
		Health:           statusOf(app.Status.Health.Status),
		AutoSync:         app.AutoSyncEnabled(),
		DeployedRevision: cut(app.DeployedRevision(), maxNameLen),
		ComparedRevision: cut(app.Status.Sync.Revision, maxNameLen),
		URL:              s.client.ApplicationURL(ref),
	}

	if history := app.HistoryNewestFirst(); len(history) > 0 {
		summary.LastSyncedAt = history[0].DeployedAt
	}

	return summary
}

// newListFilter checks the input of a call and returns its filter.
func newListFilter(in listInput) (listFilter, error) {
	f := listFilter{project: strings.TrimSpace(in.Project), name: strings.TrimSpace(in.Name), state: in.State}

	if f.project != "" && !projectPattern.MatchString(f.project) {
		return listFilter{}, &source.ToolError{Message: fmt.Sprintf("project: %q is not a project name", cut(f.project, maxNameLen))}
	}

	if _, err := path.Match(f.name, ""); err != nil {
		return listFilter{}, &source.ToolError{Message: fmt.Sprintf("name: %q is not a valid pattern", cut(f.name, maxNameLen))}
	}

	switch f.state {
	case "", stateAny:
		f.state = stateAny
	case stateOutOfSync, stateNotHealthy, stateProblem:
	default:
		return listFilter{}, &source.ToolError{Message: fmt.Sprintf("state: %q must be any, out_of_sync, not_healthy or problem",
			cut(f.state, maxNameLen))}
	}

	return f, nil
}

// matches reports whether the application passes the filter.
func (f listFilter) matches(app *argo.Application) bool {
	if f.project != "" && app.Ref().Project != f.project {
		return false
	}

	if f.name != "" {
		if ok, err := path.Match(f.name, app.Metadata.Name); err != nil || !ok {
			return false
		}
	}

	outOfSync := app.Status.Sync.Status == syncOutOfSync
	notHealthy := app.Status.Health.Status != healthHealthy

	switch f.state {
	case stateOutOfSync:
		return outOfSync
	case stateNotHealthy:
		return notHealthy
	case stateProblem:
		return outOfSync || notHealthy
	default:
		return true
	}
}

// describe returns the filter in words, for the text of an empty result.
func (f listFilter) describe() string {
	var parts []string

	if f.project != "" {
		parts = append(parts, "project "+f.project)
	}

	if f.name != "" {
		parts = append(parts, "name "+f.name)
	}

	if f.state != stateAny {
		parts = append(parts, "state "+f.state)
	}

	if len(parts) == 0 {
		return " was found"
	}

	return " matches the filter (" + strings.Join(parts, ", ") + ")"
}

// destinationOf returns the destination of an application, naming the
// cluster by name or, without one, by server.
func destinationOf(d argo.Destination) destination {
	cluster := d.Name
	if cluster == "" {
		cluster = d.Server
	}

	return destination{Cluster: cut(cluster, maxNameLen), Namespace: cut(d.Namespace, maxNameLen)}
}

// statusOf returns a status Argo CD gives, cut, or Unknown when it gives
// none.
func statusOf(s string) string {
	if s == "" {
		return statusUnknown
	}

	return cut(s, maxNameLen)
}

package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on how much of an application a result keeps.
const (
	// maxResources bounds the resources that are not in order.
	maxResources = 20
	// maxConditions bounds the conditions.
	maxConditions = 10
	// maxListItems bounds the images, value files, parameters and other
	// lists of short texts.
	maxListItems = 20
	// maxParameterLen bounds a Helm parameter, as name=value.
	maxParameterLen = 160
)

// hiddenValue replaces the value of a Helm parameter whose name looks like a
// secret. Argo CD keeps parameters in plain text, and the guard redacts only
// the secrets it recognizes, so a random password would reach the model.
const hiddenValue = "[hidden]"

// secretNameWords are the words that make a Helm parameter name look like a
// secret when the last dotted segment of the name, in any case, holds one of
// them, such as db.password, apiKey, tls.key or auth.existingSecret.
var secretNameWords = []string{"password", "passwd", "secret", "token", "key", "credential", "auth", "cert"}

// notRecordedText says why a result has no per-resource health.
const notRecordedText = "Argo CD did not give the health of each resource, so only resources that are not synced are listed."

// applicationDetail is the result of get_application.
type applicationDetail struct {
	// Application is project/name, the form the tools take.
	Application string `json:"application"`
	Name        string `json:"name"`
	Project     string `json:"project"`
	// Source is set for an application with one source, Sources for one
	// with several, and SourceHydrator for one that syncs hydrated
	// manifests.
	Source           *sourceView    `json:"source,omitempty"`
	Sources          []sourceView   `json:"sources,omitempty"`
	SourceHydrator   *hydratorView  `json:"sourceHydrator,omitempty"`
	Destination      destination    `json:"destination"`
	SyncPolicy       syncPolicyView `json:"syncPolicy"`
	SyncStatus       string         `json:"syncStatus"`
	Health           string         `json:"health"`
	DeployedRevision string         `json:"deployedRevision,omitempty"`
	ComparedRevision string         `json:"comparedRevision,omitempty"`
	LastSyncedAt     time.Time      `json:"lastSyncedAt,omitzero"`
	// OperationRunning says that a sync or rollback is requested or running.
	OperationRunning bool            `json:"operationRunning"`
	LastOperation    *operationView  `json:"lastOperation,omitempty"`
	Conditions       []conditionView `json:"conditions,omitempty"`
	Images           []string        `json:"images,omitempty"`
	// ProblemResourceCount is the number of resources that are not synced
	// or not healthy; ProblemResources lists the first of them.
	ProblemResourceCount int            `json:"problemResourceCount"`
	ProblemResources     []resourceView `json:"problemResources,omitempty"`
	// ResourceNote says when the resources have no health.
	ResourceNote string `json:"resourceNote,omitempty"`
	// RevisionHistoryLimit is how many deployments Argo CD keeps in the
	// history.
	RevisionHistoryLimit int    `json:"revisionHistoryLimit"`
	URL                  string `json:"url"`
}

// sourceView is a source of an application.
type sourceView struct {
	RepoURL        string         `json:"repoURL"`
	Path           string         `json:"path,omitempty"`
	Chart          string         `json:"chart,omitempty"`
	TargetRevision string         `json:"targetRevision,omitempty"`
	Helm           *helmView      `json:"helm,omitempty"`
	Kustomize      *kustomizeView `json:"kustomize,omitempty"`
}

// helmView is the Helm settings of a source, without inline values.
type helmView struct {
	ReleaseName string   `json:"releaseName,omitempty"`
	ValueFiles  []string `json:"valueFiles,omitempty"`
	// Parameters are the parameters set in the application, as name=value.
	Parameters []string `json:"parameters,omitempty"`
}

// kustomizeView is the Kustomize settings of a source.
type kustomizeView struct {
	NamePrefix string   `json:"namePrefix,omitempty"`
	NameSuffix string   `json:"nameSuffix,omitempty"`
	Images     []string `json:"images,omitempty"`
	Version    string   `json:"version,omitempty"`
}

// hydratorView is the source hydrator settings of an application.
type hydratorView struct {
	DryRepoURL        string `json:"dryRepoURL"`
	DryPath           string `json:"dryPath,omitempty"`
	DryTargetRevision string `json:"dryTargetRevision,omitempty"`
	SyncBranch        string `json:"syncBranch,omitempty"`
	SyncPath          string `json:"syncPath,omitempty"`
}

// syncPolicyView says when the application syncs.
type syncPolicyView struct {
	// AutoSync says whether Argo CD syncs the application by itself.
	AutoSync bool `json:"autoSync"`
	// Summary says it in a sentence.
	Summary     string   `json:"summary"`
	Prune       bool     `json:"prune"`
	SelfHeal    bool     `json:"selfHeal"`
	SyncOptions []string `json:"syncOptions,omitempty"`
}

// operationView is the latest sync or rollback of an application.
type operationView struct {
	Phase      string    `json:"phase"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	RetryCount int64     `json:"retryCount,omitempty"`
	// InitiatedBy is user:<name> or automated.
	InitiatedBy string `json:"initiatedBy,omitempty"`
	Revision    string `json:"revision,omitempty"`
}

// conditionView is a warning or error Argo CD reports about an
// application.
type conditionView struct {
	Type               string    `json:"type"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime,omitzero"`
}

// resourceView is a resource that is not synced or not healthy.
type resourceView struct {
	Group           string `json:"group,omitempty"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	SyncStatus      string `json:"syncStatus,omitempty"`
	Health          string `json:"health,omitempty"`
	RequiresPruning bool   `json:"requiresPruning,omitempty"`
}

// getApplication runs get_application.
func (s *Source) getApplication(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[applicationInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	ref, err := s.resolve(in.Application)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	app, err := s.client.GetApplication(ctx, ref)
	if err != nil {
		return source.Result{}, fmt.Errorf("read application %s: %w", ref, err)
	}

	detail := s.detail(app)

	data, err := json.Marshal(detail)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceResource,
		Title: "Argo CD application " + detail.Application,
		URL:   detail.URL,
		At:    detail.LastSyncedAt,
	}}

	return source.Result{Structured: data, Evidence: evidence, Truncated: detail.ProblemResourceCount > len(detail.ProblemResources)}, nil
}

// detail returns the get_application result of an application.
func (s *Source) detail(app *argo.Application) applicationDetail {
	ref := app.Ref()
	summary := s.summarize(app)

	d := applicationDetail{
		Application:          ref.String(),
		Name:                 ref.Name,
		Project:              ref.Project,
		Destination:          summary.Destination,
		SyncPolicy:           syncPolicyOf(app),
		SyncStatus:           summary.SyncStatus,
		Health:               summary.Health,
		DeployedRevision:     summary.DeployedRevision,
		ComparedRevision:     summary.ComparedRevision,
		LastSyncedAt:         summary.LastSyncedAt,
		OperationRunning:     app.OperationRunning(),
		LastOperation:        operationOf(app.Status.OperationState),
		Conditions:           conditionsOf(app.Status.Conditions),
		Images:               cutAll(app.Status.Summary.Images, maxListItems, maxURLLen),
		RevisionHistoryLimit: app.HistoryLimit(),
		URL:                  summary.URL,
	}

	switch {
	case app.MultiSource():
		for i := range app.Spec.Sources {
			d.Sources = append(d.Sources, sourceOf(&app.Spec.Sources[i]))
		}
	case app.Spec.Source != nil:
		v := sourceOf(app.Spec.Source)
		d.Source = &v
	case app.Spec.SourceHydrator != nil:
		d.SourceHydrator = hydratorOf(app.Spec.SourceHydrator)
	}

	d.ProblemResources, d.ProblemResourceCount, d.ResourceNote = problemResources(app.Status)

	return d
}

// syncPolicyOf returns the sync policy of an application, with a sentence
// that says whether auto-sync is on.
func syncPolicyOf(app *argo.Application) syncPolicyView {
	view := syncPolicyView{AutoSync: app.AutoSyncEnabled()}

	policy := app.Spec.SyncPolicy
	if policy == nil {
		view.Summary = "Auto-sync is OFF: the application changes only when a person or a pipeline syncs it."

		return view
	}

	view.SyncOptions = cutAll(policy.SyncOptions, maxListItems, maxNameLen)

	switch auto := policy.Automated; {
	case auto == nil:
		view.Summary = "Auto-sync is OFF: the application changes only when a person or a pipeline syncs it."
	case !view.AutoSync:
		view.Summary = "Auto-sync is OFF: an automated policy is set but switched off with enabled: false, so the application " +
			"changes only when a person or a pipeline syncs it."
	default:
		view.Prune = auto.Prune != nil && *auto.Prune
		view.SelfHeal = auto.SelfHeal != nil && *auto.SelfHeal
		view.Summary = fmt.Sprintf("Auto-sync is ON: Argo CD syncs the application to its target revision by itself "+
			"(prune: %t, self-heal: %t). Argo CD refuses a rollback while auto-sync is on, and the next sync would undo it.",
			view.Prune, view.SelfHeal)
	}

	return view
}

// operationOf returns the latest operation, or nil when there is none.
func operationOf(state *argo.OperationState) *operationView {
	if state == nil {
		return nil
	}

	view := &operationView{
		Phase:       cut(state.Phase, maxNameLen),
		Message:     cut(state.Message, maxMessageLen),
		StartedAt:   state.StartedAt,
		RetryCount:  state.RetryCount,
		InitiatedBy: state.Operation.InitiatedBy.String(),
	}

	if state.FinishedAt != nil {
		view.FinishedAt = *state.FinishedAt
	}

	if state.SyncResult != nil {
		view.Revision = cut(state.SyncResult.Revision, maxNameLen)
	}

	return view
}

// conditionsOf returns the first conditions of an application.
func conditionsOf(conditions []argo.Condition) []conditionView {
	var views []conditionView

	for _, c := range conditions[:min(len(conditions), maxConditions)] {
		view := conditionView{Type: cut(c.Type, maxNameLen), Message: cut(c.Message, maxMessageLen)}
		if c.LastTransitionTime != nil {
			view.LastTransitionTime = *c.LastTransitionTime
		}

		views = append(views, view)
	}

	return views
}

// sourceOf returns a source of an application.
func sourceOf(src *argo.Source) sourceView {
	view := sourceView{
		RepoURL:        cut(src.RepoURL, maxURLLen),
		Path:           cut(src.Path, maxNameLen),
		Chart:          cut(src.Chart, maxNameLen),
		TargetRevision: cut(src.TargetRevision, maxNameLen),
	}

	if h := src.Helm; h != nil {
		hv := &helmView{ReleaseName: cut(h.ReleaseName, maxNameLen), ValueFiles: cutAll(h.ValueFiles, maxListItems, maxNameLen)}

		for _, p := range h.Parameters[:min(len(h.Parameters), maxListItems)] {
			hv.Parameters = append(hv.Parameters, parameterText(p))
		}

		view.Helm = hv
	}

	if k := src.Kustomize; k != nil {
		view.Kustomize = &kustomizeView{
			NamePrefix: cut(k.NamePrefix, maxNameLen),
			NameSuffix: cut(k.NameSuffix, maxNameLen),
			Images:     cutAll(k.Images, maxListItems, maxNameLen),
			Version:    cut(k.Version, maxNameLen),
		}
	}

	return view
}

// parameterText returns a Helm parameter as name=value, with the value
// replaced by hiddenValue when the name looks like a secret.
func parameterText(p argo.HelmParameter) string {
	value := p.Value
	if looksSecret(p.Name) {
		value = hiddenValue
	}

	return cut(p.Name+"="+value, maxParameterLen)
}

// looksSecret reports whether the last dotted segment of a Helm parameter
// name, in any case, holds one of secretNameWords.
func looksSecret(name string) bool {
	last := strings.ToLower(name[strings.LastIndex(name, ".")+1:])

	return slices.ContainsFunc(secretNameWords, func(word string) bool { return strings.Contains(last, word) })
}

// hydratorOf returns the source hydrator settings of an application.
func hydratorOf(h *argo.SourceHydrator) *hydratorView {
	return &hydratorView{
		DryRepoURL:        cut(h.DrySource.RepoURL, maxURLLen),
		DryPath:           cut(h.DrySource.Path, maxNameLen),
		DryTargetRevision: cut(h.DrySource.TargetRevision, maxNameLen),
		SyncBranch:        cut(h.SyncSource.TargetBranch, maxNameLen),
		SyncPath:          cut(h.SyncSource.Path, maxNameLen),
	}
}

// problemResources returns the first maxResources resources that are not
// synced or not healthy, how many there are in all, and a note when the
// health of the resources is not recorded.
func problemResources(status argo.Status) (resources []resourceView, count int, note string) {
	healthKnown := status.ResourceHealthSource == ""

	for i := range status.Resources {
		r := &status.Resources[i]
		if r.Health != nil {
			healthKnown = true
		}
	}

	for i := range status.Resources {
		r := &status.Resources[i]

		health := ""
		if r.Health != nil {
			health = r.Health.Status
		}

		if (r.Status == "" || r.Status == "Synced") && (health == "" || health == healthHealthy) {
			continue
		}

		count++

		if len(resources) < maxResources {
			resources = append(resources, resourceView{
				Group:           cut(r.Group, maxNameLen),
				Kind:            cut(r.Kind, maxNameLen),
				Namespace:       cut(r.Namespace, maxNameLen),
				Name:            cut(r.Name, maxNameLen),
				SyncStatus:      cut(r.Status, maxNameLen),
				Health:          cut(health, maxNameLen),
				RequiresPruning: r.RequiresPruning,
			})
		}
	}

	if !healthKnown && len(status.Resources) > 0 {
		note = notRecordedText
	}

	return resources, count, note
}

// cutAll returns at most n of the texts, each cut to at most size bytes.
func cutAll(texts []string, n, size int) []string {
	var out []string

	for _, t := range texts[:min(len(texts), n)] {
		out = append(out, cut(t, size))
	}

	return out
}

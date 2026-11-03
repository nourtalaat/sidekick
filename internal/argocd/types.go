package argocd

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// defaultProject is the project of an application whose spec names none.
const defaultProject = "default"

// defaultHistoryLimit is how many history entries Argo CD keeps when an
// application sets no revisionHistoryLimit.
const defaultHistoryLimit = 10

// Operation phases of an Argo CD operation that has not finished.
const (
	phaseRunning     = "Running"
	phaseTerminating = "Terminating"
)

// Operation phases of an operation that has ended, as gitops-engine names
// them.
const (
	phaseSucceeded = "Succeeded"
	phaseFailed    = "Failed"
	phaseError     = "Error"
)

// Application is the part of an Argo CD application that Sidekick reads.
type Application struct {
	Metadata Metadata `json:"metadata"`
	Spec     Spec     `json:"spec"`
	Status   Status   `json:"status"`
	// Operation is an operation that was asked for and has not finished.
	// Argo CD's controller removes it when the operation ends and records
	// the result in Status.OperationState.
	Operation *Operation `json:"operation,omitempty"`
}

// Metadata is the Kubernetes metadata of an application.
type Metadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// Spec is what an application is meant to be.
type Spec struct {
	// Project is empty for an application of the default project.
	Project string `json:"project"`
	// Source is set for an application with one source.
	Source *Source `json:"source,omitempty"`
	// Sources is set for an application with several sources, and for one
	// that lists a single source under sources.
	Sources []Source `json:"sources,omitempty"`
	// SourceHydrator is set for an application that syncs hydrated
	// manifests; it has no Source.
	SourceHydrator *SourceHydrator `json:"sourceHydrator,omitempty"`
	Destination    Destination     `json:"destination"`
	SyncPolicy     *SyncPolicy     `json:"syncPolicy,omitempty"`
	// RevisionHistoryLimit is how many history entries Argo CD keeps. Nil
	// means 10.
	RevisionHistoryLimit *int64 `json:"revisionHistoryLimit,omitempty"`
}

// Source is where an application's manifests come from.
type Source struct {
	RepoURL        string           `json:"repoURL"`
	Path           string           `json:"path,omitempty"`
	TargetRevision string           `json:"targetRevision,omitempty"`
	Chart          string           `json:"chart,omitempty"`
	Helm           *HelmSource      `json:"helm,omitempty"`
	Kustomize      *KustomizeSource `json:"kustomize,omitempty"`
}

// HelmSource is the Helm settings of a source. Inline values are not read.
type HelmSource struct {
	ReleaseName string          `json:"releaseName,omitempty"`
	ValueFiles  []string        `json:"valueFiles,omitempty"`
	Parameters  []HelmParameter `json:"parameters,omitempty"`
}

// HelmParameter is one Helm parameter set in an application.
type HelmParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// KustomizeSource is the Kustomize settings of a source.
type KustomizeSource struct {
	NamePrefix string   `json:"namePrefix,omitempty"`
	NameSuffix string   `json:"nameSuffix,omitempty"`
	Images     []string `json:"images,omitempty"`
	Version    string   `json:"version,omitempty"`
}

// SourceHydrator is the settings of an application that syncs hydrated
// manifests.
type SourceHydrator struct {
	DrySource  DrySource  `json:"drySource"`
	SyncSource SyncSource `json:"syncSource"`
}

// DrySource is where the manifests that get hydrated come from.
type DrySource struct {
	RepoURL        string `json:"repoURL"`
	TargetRevision string `json:"targetRevision"`
	Path           string `json:"path"`
}

// SyncSource is where the hydrated manifests are synced from.
type SyncSource struct {
	TargetBranch string `json:"targetBranch"`
	Path         string `json:"path"`
}

// Destination is where an application deploys to. Name and Server are two
// ways to name the cluster.
type Destination struct {
	Server    string `json:"server,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// SyncPolicy says when and how an application syncs.
type SyncPolicy struct {
	Automated   *Automated `json:"automated,omitempty"`
	SyncOptions []string   `json:"syncOptions,omitempty"`
}

// Automated is the automated sync policy. It may be present and switched
// off with Enabled set to false.
type Automated struct {
	Enabled    *bool `json:"enabled,omitempty"`
	Prune      *bool `json:"prune,omitempty"`
	SelfHeal   *bool `json:"selfHeal,omitempty"`
	AllowEmpty *bool `json:"allowEmpty,omitempty"`
}

// Status is what Argo CD observes about an application.
type Status struct {
	Sync   SyncStatus `json:"sync"`
	Health Health     `json:"health"`
	// History holds the successful syncs of the whole application, oldest
	// first, as many as the revision history limit.
	History        []HistoryEntry   `json:"history,omitempty"`
	Conditions     []Condition      `json:"conditions,omitempty"`
	OperationState *OperationState  `json:"operationState,omitempty"`
	Summary        Summary          `json:"summary"`
	Resources      []ResourceStatus `json:"resources,omitempty"`
	// ResourceHealthSource is "appTree" when Argo CD keeps the health of
	// each resource out of the application, so a list of applications has no
	// per-resource health.
	ResourceHealthSource string     `json:"resourceHealthSource,omitempty"`
	ReconciledAt         *time.Time `json:"reconciledAt,omitempty"`
}

// SyncStatus compares the live state with the desired state.
type SyncStatus struct {
	// Status is Synced, OutOfSync or Unknown.
	Status string `json:"status"`
	// Revision is the revision the comparison was made against, such as the
	// newest commit of the target branch. It is not the deployed revision.
	Revision  string   `json:"revision,omitempty"`
	Revisions []string `json:"revisions,omitempty"`
}

// Health is the health Argo CD derives for an application.
type Health struct {
	// Status is Healthy, Progressing, Degraded, Suspended, Missing or
	// Unknown.
	Status string `json:"status"`
}

// HistoryEntry is one successful sync of the whole application.
type HistoryEntry struct {
	// ID grows with each entry, so the newest entry has the highest ID.
	ID int64 `json:"id"`
	// Revision is the synced revision of an application with one source.
	Revision string `json:"revision,omitempty"`
	// Revisions are the synced revisions of an application with sources,
	// in the order of Sources.
	Revisions []string `json:"revisions,omitempty"`
	// DeployedAt is when the sync finished.
	DeployedAt      time.Time  `json:"deployedAt"`
	DeployStartedAt *time.Time `json:"deployStartedAt,omitempty"`
	Source          Source     `json:"source"`
	Sources         []Source   `json:"sources,omitempty"`
	// InitiatedBy is empty for an entry that an old Argo CD wrote.
	InitiatedBy Initiator `json:"initiatedBy"`
}

// Initiator says who started an operation: a user, or the application
// controller.
type Initiator struct {
	Username  string `json:"username,omitempty"`
	Automated bool   `json:"automated,omitempty"`
}

// Condition is a warning or error Argo CD reports about an application.
type Condition struct {
	Type               string     `json:"type"`
	Message            string     `json:"message"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// OperationState is the state of the latest operation.
type OperationState struct {
	Operation  Operation   `json:"operation"`
	Phase      string      `json:"phase"`
	Message    string      `json:"message,omitempty"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	RetryCount int64       `json:"retryCount,omitempty"`
	SyncResult *SyncResult `json:"syncResult,omitempty"`
}

// SyncResult is the result of a finished sync: the revision it deployed.
type SyncResult struct {
	Revision string `json:"revision"`
}

// Operation is an operation of an application: who started it and, for a
// sync or rollback, the revision it deploys.
type Operation struct {
	InitiatedBy Initiator `json:"initiatedBy"`
	// Sync is set for a sync or a rollback, which is a sync of an older
	// history entry.
	Sync *SyncOperation `json:"sync,omitempty"`
}

// SyncOperation is the part of a sync operation that Sidekick reads.
type SyncOperation struct {
	// Revision is the revision the operation syncs to. Argo CD sets it to
	// the revision of the history entry for a rollback.
	Revision string `json:"revision,omitempty"`
}

// Summary lists what an application runs.
type Summary struct {
	Images       []string `json:"images,omitempty"`
	ExternalURLs []string `json:"externalURLs,omitempty"`
}

// ResourceStatus is the sync status, and the health when Argo CD gives it,
// of one resource of an application.
type ResourceStatus struct {
	Group           string  `json:"group,omitempty"`
	Kind            string  `json:"kind"`
	Namespace       string  `json:"namespace,omitempty"`
	Name            string  `json:"name"`
	Status          string  `json:"status,omitempty"`
	Health          *Health `json:"health,omitempty"`
	Hook            bool    `json:"hook,omitempty"`
	RequiresPruning bool    `json:"requiresPruning,omitempty"`
}

// RevisionMetadata is what Argo CD's repository server knows about one
// revision.
type RevisionMetadata struct {
	Author  string     `json:"author,omitempty"`
	Date    *time.Time `json:"date,omitempty"`
	Tags    []string   `json:"tags,omitempty"`
	Message string     `json:"message,omitempty"`
}

// Ref returns the application's project and name. An application with no
// project is in the project "default".
func (a *Application) Ref() AppRef {
	project := a.Spec.Project
	if project == "" {
		project = defaultProject
	}

	return AppRef{Project: project, Name: a.Metadata.Name}
}

// AutoSyncEnabled reports whether Argo CD syncs the application by itself.
// It is true when an automated policy is set and not switched off with
// enabled: false, which is how Argo CD decides it too.
func (a *Application) AutoSyncEnabled() bool {
	p := a.Spec.SyncPolicy

	return p != nil && p.Automated != nil && (p.Automated.Enabled == nil || *p.Automated.Enabled)
}

// OperationRunning reports whether an operation is requested or running.
func (a *Application) OperationRunning() bool {
	if a.Operation != nil {
		return true
	}

	s := a.Status.OperationState

	return s != nil && (s.Phase == phaseRunning || s.Phase == phaseTerminating)
}

// Completed reports whether the operation has ended, in any way.
func (s *OperationState) Completed() bool {
	return s.Phase == phaseSucceeded || s.Phase == phaseFailed || s.Phase == phaseError
}

// Succeeded reports whether the operation ended well.
func (s *OperationState) Succeeded() bool {
	return s.Phase == phaseSucceeded
}

// PendingRevision returns the revision of the sync or rollback that is
// requested or running, or "" when there is none, or when it names no
// revision.
func (a *Application) PendingRevision() string {
	if a.Operation != nil && a.Operation.Sync != nil {
		return a.Operation.Sync.Revision
	}

	if s := a.Status.OperationState; s != nil && !s.Completed() && s.Operation.Sync != nil {
		return s.Operation.Sync.Revision
	}

	return ""
}

// MultiSource reports whether the application lists its sources under
// sources, which is how Argo CD decides that history entries hold sources
// and revisions instead of one source and one revision.
func (a *Application) MultiSource() bool {
	return len(a.Spec.Sources) > 0
}

// HistoryLimit returns how many history entries Argo CD keeps for the
// application.
func (a *Application) HistoryLimit() int {
	if a.Spec.RevisionHistoryLimit != nil {
		return int(*a.Spec.RevisionHistoryLimit)
	}

	return defaultHistoryLimit
}

// HistoryNewestFirst returns the history entries ordered by ID, the
// highest first. Argo CD stores them oldest first.
func (a *Application) HistoryNewestFirst() []HistoryEntry {
	entries := slices.Clone(a.Status.History)
	slices.SortFunc(entries, func(x, y HistoryEntry) int { return cmp.Compare(y.ID, x.ID) })

	return entries
}

// DeployedRevision returns the revision the application runs: the one of
// the newest history entry, or the one of the last sync when there is no
// history. It is empty for an application that never synced.
func (a *Application) DeployedRevision() string {
	if h := a.HistoryNewestFirst(); len(h) > 0 {
		return h[0].RevisionText()
	}

	if s := a.Status.OperationState; s != nil && s.SyncResult != nil {
		return s.SyncResult.Revision
	}

	return ""
}

// MultiSource reports whether the entry holds sources and revisions
// instead of one source and one revision.
func (h HistoryEntry) MultiSource() bool {
	return len(h.Sources) > 0
}

// RevisionText returns the entry's revision, or its revisions joined by
// commas when it has several.
func (h HistoryEntry) RevisionText() string {
	if h.Revision != "" {
		return h.Revision
	}

	return strings.Join(h.Revisions, ",")
}

// String returns "user:<name>", "automated", or "" when nothing is known.
func (i Initiator) String() string {
	switch {
	case i.Automated:
		return "automated"
	case i.Username != "":
		return "user:" + i.Username
	default:
		return ""
	}
}

package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the history a result holds.
const (
	// maxHistory bounds the entries of a result. Argo CD keeps 10 by
	// default, and more only when the application asks for it.
	maxHistory = 50
	// maxCommits is how many of the newest entries get the commit of their
	// revision.
	maxCommits = 10
	// metadataConcurrency is how many commits are read at once.
	metadataConcurrency = 5
)

// historyResult is the result of get_history.
type historyResult struct {
	Application string `json:"application"`
	// AutoSync says whether Argo CD syncs the application by itself.
	AutoSync bool `json:"autoSync"`
	// RevisionHistoryLimit is how many entries Argo CD keeps.
	RevisionHistoryLimit int `json:"revisionHistoryLimit"`
	// Entries are the deployments, newest first.
	Entries []historyView `json:"entries"`
	// CommitsUnavailable is the number of entries that should have a commit
	// and have none, because Argo CD could not read the revision.
	CommitsUnavailable int    `json:"commitsUnavailable,omitempty"`
	URL                string `json:"url"`
}

// historyView is one deployment of an application.
type historyView struct {
	ID int64 `json:"id"`
	// Current marks the entry that is deployed now, the newest one.
	Current bool `json:"current,omitempty"`
	// Revision is the deployed revision of an application with one source,
	// Revisions those of an application with several.
	Revision        string       `json:"revision,omitempty"`
	Revisions       []string     `json:"revisions,omitempty"`
	DeployedAt      time.Time    `json:"deployedAt,omitzero"`
	DeployStartedAt time.Time    `json:"deployStartedAt,omitzero"`
	InitiatedBy     string       `json:"initiatedBy,omitempty"`
	Source          *sourceView  `json:"source,omitempty"`
	Sources         []sourceView `json:"sources,omitempty"`
	// Commit is what Argo CD's repository server knows about the revision.
	Commit *commitView `json:"commit,omitempty"`
}

// commitView is the commit of a revision.
type commitView struct {
	Message string    `json:"message,omitempty"`
	Author  string    `json:"author,omitempty"`
	Date    time.Time `json:"date,omitzero"`
	Tags    []string  `json:"tags,omitempty"`
}

// getHistory runs get_history.
func (s *Source) getHistory(ctx context.Context, input json.RawMessage) (source.Result, error) {
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

	history := app.HistoryNewestFirst()
	link := s.client.ApplicationURL(ref)

	if len(history) == 0 {
		return source.Result{
			NoData:   true,
			Text:     fmt.Sprintf("Argo CD has no deployment history for %s: it never synced, or its revisionHistoryLimit is 0.", ref),
			Evidence: []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Argo CD history of " + ref.String(), URL: link}},
		}, nil
	}

	truncated := len(history) > maxHistory
	history = history[:min(len(history), maxHistory)]

	result := historyResult{
		Application:          ref.String(),
		AutoSync:             app.AutoSyncEnabled(),
		RevisionHistoryLimit: app.HistoryLimit(),
		Entries:              make([]historyView, 0, len(history)),
		URL:                  link,
	}

	for i := range history {
		result.Entries = append(result.Entries, historyOf(&history[i], i == 0))
	}

	result.CommitsUnavailable, err = s.addCommits(ctx, ref, history, result.Entries)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the commits of %s: %w", ref, err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	evidence := []source.Evidence{{Kind: source.EvidenceDeploy, Title: "Argo CD history of " + ref.String(), URL: link, At: history[0].DeployedAt}}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// addCommits reads the commit of each of the newest entries that has one
// revision, a few at a time, into views, and returns how many it could not
// read. An entry with several sources gets none, and a commit that cannot be
// read is left out, so a repository that Argo CD cannot reach does not fail
// the call. Only the end of ctx does.
func (s *Source) addCommits(ctx context.Context, ref argo.AppRef, history []argo.HistoryEntry, views []historyView) (int, error) {
	var group errgroup.Group

	group.SetLimit(metadataConcurrency)

	wanted := 0

	for i := range min(len(history), maxCommits) {
		if history[i].MultiSource() || history[i].Revision == "" {
			continue
		}

		wanted++

		group.Go(func() error {
			views[i].Commit = s.commitOf(ctx, ref, &history[i])

			return nil
		})
	}

	_ = group.Wait()

	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("wait for the commits: %w", err)
	}

	found := 0

	for i := range views {
		if views[i].Commit != nil {
			found++
		}
	}

	return wanted - found, nil
}

// commitOf reads the commit of an entry's revision. It returns nil when
// Argo CD cannot give it.
func (s *Source) commitOf(ctx context.Context, ref argo.AppRef, entry *argo.HistoryEntry) *commitView {
	meta, err := s.client.RevisionMetadata(ctx, ref, entry.Revision, entry.ID)
	if err != nil {
		return nil
	}

	view := &commitView{
		Message: cut(meta.Message, maxMessageLen),
		Author:  cut(meta.Author, maxNameLen),
		Tags:    cutAll(meta.Tags, maxListItems, maxNameLen),
	}

	if meta.Date != nil {
		view.Date = *meta.Date
	}

	return view
}

// historyOf returns the view of a history entry.
func historyOf(entry *argo.HistoryEntry, current bool) historyView {
	view := historyView{
		ID:          entry.ID,
		Current:     current,
		DeployedAt:  entry.DeployedAt,
		InitiatedBy: entry.InitiatedBy.String(),
	}

	if entry.DeployStartedAt != nil {
		view.DeployStartedAt = *entry.DeployStartedAt
	}

	if entry.MultiSource() {
		view.Revisions = cutAll(entry.Revisions, maxListItems, maxNameLen)

		for i := range entry.Sources[:min(len(entry.Sources), maxListItems)] {
			view.Sources = append(view.Sources, sourceOf(&entry.Sources[i]))
		}

		return view
	}

	view.Revision = cut(entry.Revision, maxNameLen)

	if entry.Source.RepoURL != "" {
		src := sourceOf(&entry.Source)
		view.Source = &src
	}

	return view
}

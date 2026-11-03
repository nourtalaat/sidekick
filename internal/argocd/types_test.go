package argocd

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplicationAutoSyncEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy *SyncPolicy
		want   bool
	}{
		{name: "no sync policy"},
		{name: "a policy without automated sync", policy: &SyncPolicy{SyncOptions: []string{"CreateNamespace=true"}}},
		{name: "automated with no keys", policy: &SyncPolicy{Automated: &Automated{}}, want: true},
		{name: "automated with prune and self-heal", policy: &SyncPolicy{Automated: &Automated{Prune: new(true), SelfHeal: new(true)}}, want: true},
		{name: "automated and enabled", policy: &SyncPolicy{Automated: &Automated{Enabled: new(true)}}, want: true},
		{name: "automated and switched off", policy: &SyncPolicy{Automated: &Automated{Enabled: new(false), Prune: new(true)}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := Application{Spec: Spec{SyncPolicy: tt.policy}}
			require.Equal(t, tt.want, app.AutoSyncEnabled())
		})
	}
}

func TestApplicationOperationRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		app  Application
		want bool
	}{
		{name: "no operation"},
		{name: "an operation that was asked for", app: Application{Operation: &Operation{}}, want: true},
		{name: "a running operation", app: Application{Status: Status{OperationState: &OperationState{Phase: "Running"}}}, want: true},
		{name: "a terminating operation", app: Application{Status: Status{OperationState: &OperationState{Phase: "Terminating"}}}, want: true},
		{name: "a finished operation", app: Application{Status: Status{OperationState: &OperationState{Phase: "Succeeded"}}}},
		{name: "a failed operation", app: Application{Status: Status{OperationState: &OperationState{Phase: "Failed"}}}},
		{name: "an operation with an error", app: Application{Status: Status{OperationState: &OperationState{Phase: "Error"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.app.OperationRunning())
		})
	}
}

func TestApplicationFromFixture(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("fixture/application.json")
	require.NoError(t, err)

	var app Application
	require.NoError(t, json.Unmarshal(data, &app))

	tests := []struct {
		name string
		got  func() any
		want any
	}{
		{name: "the ref", got: func() any { return app.Ref() }, want: AppRef{Project: "shop", Name: "checkout"}},
		{name: "auto-sync", got: func() any { return app.AutoSyncEnabled() }, want: true},
		{name: "one source", got: func() any { return app.MultiSource() }, want: false},
		{name: "the default history limit", got: func() any { return app.HistoryLimit() }, want: 10},
		{name: "the deployed revision", got: func() any { return app.DeployedRevision() }, want: "cccc3333cccc3333cccc3333cccc3333cccc3333"},
		{name: "history ids newest first", got: func() any {
			ids := []int64{}
			for _, h := range app.HistoryNewestFirst() {
				ids = append(ids, h.ID)
			}

			return ids
		}, want: []int64{6, 5, 4}},
		{name: "who started the entries", got: func() any {
			who := []string{}
			for _, h := range app.HistoryNewestFirst() {
				who = append(who, h.InitiatedBy.String())
			}

			return who
		}, want: []string{"", "automated", "user:ada"}},
		{name: "the history is not reordered in place", got: func() any { return app.Status.History[0].ID }, want: int64(4)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.got())
		})
	}
}

func TestApplicationHistoryLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit *int64
		want  int
	}{
		{name: "unset", want: 10},
		{name: "set", limit: new(int64(3)), want: 3},
		{name: "zero keeps no history", limit: new(int64(0)), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := Application{Spec: Spec{RevisionHistoryLimit: tt.limit}}
			require.Equal(t, tt.want, app.HistoryLimit())
		})
	}
}

func TestApplicationDeployedRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		app  Application
		want string
	}{
		{name: "never synced"},
		{name: "the newest history entry", app: Application{Status: Status{History: []HistoryEntry{{ID: 1, Revision: "old"}, {ID: 2, Revision: "new"}}}}, want: "new"},
		{
			name: "several sources",
			app:  Application{Status: Status{History: []HistoryEntry{{ID: 1, Revisions: []string{"a", "b"}, Sources: []Source{{}, {}}}}}},
			want: "a,b",
		},
		{
			name: "no history but a sync result",
			app:  Application{Status: Status{OperationState: &OperationState{SyncResult: &SyncResult{Revision: "synced"}}}},
			want: "synced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.app.DeployedRevision())
		})
	}
}

func TestMultiSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		app       Application
		entry     HistoryEntry
		wantApp   bool
		wantEntry bool
	}{
		{name: "one source"},
		{
			name:    "a list of one source is several sources",
			app:     Application{Spec: Spec{Sources: []Source{{RepoURL: "a"}}}},
			entry:   HistoryEntry{Sources: []Source{{RepoURL: "a"}}},
			wantApp: true, wantEntry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantApp, tt.app.MultiSource())
			require.Equal(t, tt.wantEntry, tt.entry.MultiSource())
		})
	}
}

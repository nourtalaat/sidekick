package argocd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// checkoutPath is the path of shop/checkout in the fake API.
const checkoutPath = "/api/v1/applications/checkout"

// checkoutDetailJSON is get_application's answer for fixture/application.json,
// with {{URL}} for the base URL.
const checkoutDetailJSON = `{
	"application":"shop/checkout","name":"checkout","project":"shop",
	"source":{"repoURL":"https://git.example.com/shop/deploy.git","path":"apps/checkout","targetRevision":"main",
		"helm":{"releaseName":"checkout","valueFiles":["values-prod.yaml"],"parameters":["image.tag=v1.4.2"]}},
	"destination":{"cluster":"prod","namespace":"shop"},
	"syncPolicy":{"autoSync":true,"prune":true,"selfHeal":true,"syncOptions":["CreateNamespace=true"],
		"summary":"Auto-sync is ON: Argo CD syncs the application to its target revision by itself (prune: true, self-heal: true). Argo CD refuses a rollback while auto-sync is on, and the next sync would undo it."},
	"syncStatus":"OutOfSync","health":"Degraded","deployedRevision":"bbbb2222","comparedRevision":"ffff0000",
	"lastSyncedAt":"2026-11-02T09:00:00Z","operationRunning":false,
	"lastOperation":{"phase":"Failed","message":"one or more objects failed to apply, reason: Deployment.apps \"checkout\" is invalid",
		"startedAt":"2026-11-03T08:59:30Z","finishedAt":"2026-11-03T09:00:00Z","retryCount":2,"initiatedBy":"user:ada","revision":"cccc3333"},
	"conditions":[{"type":"SyncError","message":"one or more objects failed to apply","lastTransitionTime":"2026-11-03T09:00:00Z"}],
	"images":["registry.example.com/shop/checkout:v1.4.2"],
	"problemResourceCount":2,
	"problemResources":[
		{"group":"apps","kind":"Deployment","namespace":"shop","name":"checkout","syncStatus":"OutOfSync","health":"Degraded"},
		{"kind":"ConfigMap","namespace":"shop","name":"checkout-config","syncStatus":"OutOfSync","health":"Healthy","requiresPruning":true}],
	"revisionHistoryLimit":5,
	"url":"{{URL}}/applications/argocd/checkout"}`

// appBody returns the answer for an application shop/checkout with the
// given spec and status fields, without a leading comma.
func appBody(spec, status string) fakehttp.Response {
	return body(`{"metadata":{"name":"checkout","namespace":"argocd"},"spec":{"project":"shop"` + prefixComma(spec) +
		`},"status":{` + status + `}}`)
}

// prefixComma returns s with a comma before it, or nothing when s is empty.
func prefixComma(s string) string {
	if s == "" {
		return ""
	}

	return "," + s
}

func TestSourceGetApplication(t *testing.T) {
	t.Parallel()

	checkoutAt := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, checkoutPath, fixture("application.json"))
	api.ExpectAllCalled()

	s := newTestSource(t, api, tokenSecrets(t))

	res, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
	require.NoError(t, err)
	require.JSONEq(t, withURL(checkoutDetailJSON, api.URL()), string(res.Structured))
	require.Equal(t, []source.Evidence{{Kind: source.EvidenceResource, Title: "Argo CD application shop/checkout",
		URL: api.URL() + "/applications/argocd/checkout", At: checkoutAt}}, res.Evidence)
	require.False(t, res.Truncated)
	require.NotContains(t, string(res.Structured), "hunter2", "inline Helm values are left out")

	reqs := api.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, "shop", reqs[0].Query.Get("project"))
	require.Equal(t, "argocd", reqs[0].Query.Get("appNamespace"))
	require.Equal(t, "Bearer "+testToken, reqs[0].Header.Get("Authorization"))
}

func TestSourceGetApplicationSyncPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        string
		wantAutoOn  bool
		wantSummary string
	}{
		{
			name:        "no sync policy",
			wantSummary: "Auto-sync is OFF: the application changes only when a person or a pipeline syncs it.",
		},
		{
			name:        "a policy without automated sync",
			spec:        `"syncPolicy":{"syncOptions":["CreateNamespace=true"]}`,
			wantSummary: "Auto-sync is OFF: the application changes only when a person or a pipeline syncs it.",
		},
		{
			name: "an automated policy that is switched off",
			spec: `"syncPolicy":{"automated":{"enabled":false,"prune":true}}`,
			wantSummary: "Auto-sync is OFF: an automated policy is set but switched off with enabled: false, so the application " +
				"changes only when a person or a pipeline syncs it.",
		},
		{
			name:        "an automated policy with no keys",
			spec:        `"syncPolicy":{"automated":{}}`,
			wantAutoOn:  true,
			wantSummary: "Auto-sync is ON: Argo CD syncs the application to its target revision by itself (prune: false, self-heal: false). Argo CD refuses a rollback while auto-sync is on, and the next sync would undo it.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, appBody(tt.spec, ``))

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.NoError(t, err)

			var got applicationDetail
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Equal(t, tt.wantAutoOn, got.SyncPolicy.AutoSync)
			require.Equal(t, tt.wantSummary, got.SyncPolicy.Summary)
		})
	}
}

func TestSourceGetApplicationShapes(t *testing.T) {
	t.Parallel()

	manyResources := make([]string, 0, 25)
	for i := range 25 {
		manyResources = append(manyResources, fmt.Sprintf(`{"kind":"ConfigMap","name":"cm-%02d","status":"OutOfSync"}`, i))
	}

	tests := []struct {
		name     string
		response fakehttp.Response
		check    func(t *testing.T, got applicationDetail)
		wantCut  bool
	}{
		{
			name: "several sources",
			response: appBody(`"sources":[{"repoURL":"https://git.example.com/shop/charts.git","path":"charts/checkout","targetRevision":"v2",
				"kustomize":{"namePrefix":"eu-","images":["checkout=registry.example.com/checkout:1.2.3"]}},
				{"repoURL":"https://git.example.com/shop/values.git","targetRevision":"main"}]`, ``),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Nil(t, got.Source)
				require.Len(t, got.Sources, 2)
				require.Equal(t, "charts/checkout", got.Sources[0].Path)
				require.Equal(t, &kustomizeView{NamePrefix: "eu-", Images: []string{"checkout=registry.example.com/checkout:1.2.3"}}, got.Sources[0].Kustomize)
				require.Equal(t, "https://git.example.com/shop/values.git", got.Sources[1].RepoURL)
			},
		},
		{
			name: "a source hydrator",
			response: appBody(`"sourceHydrator":{"drySource":{"repoURL":"https://git.example.com/shop/dry.git","targetRevision":"main","path":"apps/checkout"},
				"syncSource":{"targetBranch":"env/prod","path":"hydrated/checkout"}}`, ``),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Nil(t, got.Source)
				require.Equal(t, &hydratorView{DryRepoURL: "https://git.example.com/shop/dry.git", DryPath: "apps/checkout", DryTargetRevision: "main",
					SyncBranch: "env/prod", SyncPath: "hydrated/checkout"}, got.SourceHydrator)
			},
		},
		{
			name:     "an operation that is requested but not finished",
			response: body(`{"metadata":{"name":"checkout","namespace":"argocd"},"spec":{"project":"shop"},"operation":{"sync":{}},"status":{}}`),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.True(t, got.OperationRunning)
			},
		},
		{
			name:     "an operation that is running",
			response: appBody(``, `"operationState":{"phase":"Running","startedAt":"2026-11-03T09:00:00Z","operation":{"initiatedBy":{"automated":true}}}`),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.True(t, got.OperationRunning)
				require.Equal(t, "Running", got.LastOperation.Phase)
				require.Equal(t, "automated", got.LastOperation.InitiatedBy)
				require.True(t, got.LastOperation.FinishedAt.IsZero())
			},
		},
		{
			name:     "an application that never synced",
			response: appBody(``, ``),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Empty(t, got.DeployedRevision)
				require.True(t, got.LastSyncedAt.IsZero())
				require.Equal(t, "Unknown", got.SyncStatus)
				require.Equal(t, "Unknown", got.Health)
				require.Nil(t, got.LastOperation)
				require.Equal(t, 10, got.RevisionHistoryLimit)
				require.False(t, got.OperationRunning)
			},
		},
		{
			name:     "more resources that are not in order than the result holds",
			response: appBody(``, `"resources":[`+strings.Join(manyResources, ",")+`]`),
			wantCut:  true,
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Equal(t, 25, got.ProblemResourceCount)
				require.Len(t, got.ProblemResources, maxResources)
				require.Equal(t, "cm-00", got.ProblemResources[0].Name)
				require.Empty(t, got.ResourceNote)
			},
		},
		{
			name: "health that Argo CD does not keep with the resources",
			response: appBody(``, `"resourceHealthSource":"appTree","resources":[
				{"kind":"Deployment","name":"checkout","status":"OutOfSync"},{"kind":"Service","name":"checkout","status":"Synced"}]`),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Equal(t, 1, got.ProblemResourceCount)
				require.Equal(t, notRecordedText, got.ResourceNote)
			},
		},
		{
			name: "health that Argo CD fills in from the resource tree",
			response: appBody(``, `"resourceHealthSource":"appTree","resources":[
				{"kind":"Deployment","name":"checkout","status":"Synced","health":{"status":"Progressing"}}]`),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.Equal(t, 1, got.ProblemResourceCount)
				require.Equal(t, "Progressing", got.ProblemResources[0].Health)
				require.Empty(t, got.ResourceNote)
			},
		},
		{
			name: "text from Argo CD is cut",
			response: appBody(`"source":{"repoURL":"https://git.example.com/`+strings.Repeat("x", 2*maxURLLen)+`","helm":{"parameters":[{"name":"a","value":"`+
				strings.Repeat("v", 2*maxParameterLen)+`"}]}}`,
				`"operationState":{"phase":"Failed","message":"`+strings.Repeat("é", maxMessageLen)+`","startedAt":"2026-11-03T09:00:00Z","operation":{}}`),
			check: func(t *testing.T, got applicationDetail) {
				t.Helper()

				require.LessOrEqual(t, len(got.Source.RepoURL), maxURLLen)
				require.LessOrEqual(t, len(got.Source.Helm.Parameters[0]), maxParameterLen)
				require.LessOrEqual(t, len(got.LastOperation.Message), maxMessageLen)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.NoError(t, err)
			require.Equal(t, tt.wantCut, res.Truncated)

			var got applicationDetail
			require.NoError(t, json.Unmarshal(res.Structured, &got))

			tt.check(t, got)
		})
	}
}

func TestSourceGetApplicationNoData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response fakehttp.Response
	}{
		{name: "an application Argo CD does not have", response: failed(http.StatusNotFound, `applications.argoproj.io "checkout" not found`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.NoError(t, err)
			require.True(t, res.NoData)
			require.Equal(t, notFoundText, res.Text)
			require.Empty(t, res.Structured)
			require.Equal(t, "shop", api.Requests()[0].Query.Get("project"))
		})
	}
}

func TestSourceGetApplicationRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "an application outside the globs", input: `{"application":"billing/invoices"}`,
			want: `application "billing/invoices" is not one Sidekick may read; allowed: shop/*, platform/ingress-*`},
		{name: "an application of an allowed project with a name outside the globs", input: `{"application":"platform/dns"}`,
			want: `application "platform/dns" is not one Sidekick may read`},
		{name: "a bare name", input: `{"application":"checkout"}`, want: `application "checkout" must have the form project/name`},
		{name: "a namespace in the name", input: `{"application":"shop/team-a/checkout"}`, want: "must have the form project/name"},
		{name: "a name that climbs out of the path", input: `{"application":"shop/../applicationsets"}`, want: "must have the form project/name"},
		{name: "no application", input: `{}`, want: `application "" must have the form project/name`},
		{name: "a name that is far too long", input: `{"application":"shop/` + strings.Repeat("a", 10*maxNameLen) + `"}`,
			want: "must have the form project/name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Argo CD")
		})
	}
}

func TestSourceGetApplicationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		response      fakehttp.Response
		wantToolError string
		wantErr       string
	}{
		{name: "a token that may not get the application", response: failed(http.StatusForbidden, "permission denied"), wantToolError: forbiddenText},
		{name: "a refused token", response: failed(http.StatusUnauthorized, "invalid session"), wantErr: "Argo CD answered 401: invalid session"},
		{name: "a server error", response: failed(http.StatusBadGateway, "upstream"), wantErr: "Argo CD answered 502: upstream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, checkoutPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			_, err := s.Call(t.Context(), source.Call{Tool: toolGetApplication, Input: json.RawMessage(`{"application":"shop/checkout"}`)})
			require.Error(t, err)
			require.NotContains(t, err.Error(), testToken)

			toolErr, isToolErr := errors.AsType[*source.ToolError](err)

			if tt.wantToolError != "" {
				require.True(t, isToolErr)
				require.Equal(t, tt.wantToolError, toolErr.Message)

				return
			}

			require.False(t, isToolErr)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

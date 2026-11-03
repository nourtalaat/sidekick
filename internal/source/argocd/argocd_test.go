package argocd

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The applications of fixture/applications.json that testApplications
// allows, as list_applications returns them, with {{URL}} for the base URL.
const (
	ingressJSON = `{"application":"platform/ingress-nginx","name":"ingress-nginx","project":"platform",
		"destination":{"cluster":"prod","namespace":"ingress"},"syncStatus":"Synced","health":"Healthy","autoSync":false,
		"deployedRevision":"1.9.0","comparedRevision":"1.9.0","lastSyncedAt":"2026-10-01T00:00:00Z",
		"url":"{{URL}}/applications/argocd/ingress-nginx"}`
	cartJSON = `{"application":"shop/cart","name":"cart","project":"shop",
		"destination":{"cluster":"https://kubernetes.default.svc","namespace":"shop"},"syncStatus":"Synced","health":"Healthy","autoSync":false,
		"deployedRevision":"dddd4444","comparedRevision":"dddd4444","lastSyncedAt":"2026-10-30T12:00:00Z",
		"url":"{{URL}}/applications/argocd/cart"}`
	checkoutJSON = `{"application":"shop/checkout","name":"checkout","project":"shop",
		"destination":{"cluster":"prod","namespace":"shop"},"syncStatus":"OutOfSync","health":"Degraded","autoSync":true,
		"deployedRevision":"bbbb2222","comparedRevision":"ffff0000","lastSyncedAt":"2026-11-02T09:00:00Z",
		"url":"{{URL}}/applications/argocd/checkout"}`
	workerJSON = `{"application":"shop/worker","name":"worker","project":"shop",
		"destination":{"cluster":"prod","namespace":"shop"},"syncStatus":"Synced","health":"Progressing","autoSync":false,
		"deployedRevision":"eeee5555","comparedRevision":"eeee5555","lastSyncedAt":"2026-11-03T08:00:00Z",
		"url":"{{URL}}/applications/argocd/worker"}`
)

// listEvidence returns the evidence link of an application of the list
// fixture.
func listEvidence(baseURL, application, name string, at time.Time) source.Evidence {
	return source.Evidence{Kind: source.EvidenceResource, Title: "Argo CD application " + application,
		URL: baseURL + "/applications/argocd/" + name, At: at}
}

func TestSourceListApplications(t *testing.T) {
	t.Parallel()

	ingressAt := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	cartAt := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	checkoutAt := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	workerAt := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input string
		// wantProjects are the projects the request asks for.
		wantProjects []string
		want         string
		wantEvidence func(baseURL string) []source.Evidence
		wantCut      bool
		wantNoData   string
	}{
		{
			name:         "every application the config allows, ordered",
			input:        `{}`,
			wantProjects: []string{"platform", "shop"},
			want:         `{"total":4,"applications":[` + ingressJSON + `,` + cartJSON + `,` + checkoutJSON + `,` + workerJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{
					listEvidence(u, "platform/ingress-nginx", "ingress-nginx", ingressAt),
					listEvidence(u, "shop/cart", "cart", cartAt),
					listEvidence(u, "shop/checkout", "checkout", checkoutAt),
					listEvidence(u, "shop/worker", "worker", workerAt),
				}
			},
		},
		{
			name:         "out of sync",
			input:        `{"state":"out_of_sync"}`,
			wantProjects: []string{"platform", "shop"},
			want:         `{"total":1,"applications":[` + checkoutJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{listEvidence(u, "shop/checkout", "checkout", checkoutAt)}
			},
		},
		{
			name:         "not healthy",
			input:        `{"state":"not_healthy"}`,
			wantProjects: []string{"platform", "shop"},
			want:         `{"total":2,"applications":[` + checkoutJSON + `,` + workerJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{listEvidence(u, "shop/checkout", "checkout", checkoutAt), listEvidence(u, "shop/worker", "worker", workerAt)}
			},
		},
		{
			name:         "a problem is out of sync or not healthy",
			input:        `{"state":"problem"}`,
			wantProjects: []string{"platform", "shop"},
			want:         `{"total":2,"applications":[` + checkoutJSON + `,` + workerJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{listEvidence(u, "shop/checkout", "checkout", checkoutAt), listEvidence(u, "shop/worker", "worker", workerAt)}
			},
		},
		{
			name:         "one project and a name pattern",
			input:        `{"project":"shop","name":"c*"}`,
			wantProjects: []string{"shop"},
			want:         `{"total":2,"applications":[` + cartJSON + `,` + checkoutJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{listEvidence(u, "shop/cart", "cart", cartAt), listEvidence(u, "shop/checkout", "checkout", checkoutAt)}
			},
		},
		{
			name:         "a limit cuts the list and says so",
			input:        `{"limit":1}`,
			wantProjects: []string{"platform", "shop"},
			want:         `{"total":4,"applications":[` + ingressJSON + `]}`,
			wantEvidence: func(u string) []source.Evidence {
				return []source.Evidence{listEvidence(u, "platform/ingress-nginx", "ingress-nginx", ingressAt)}
			},
			wantCut: true,
		},
		{
			name:         "no match is no data",
			input:        `{"name":"zzz*","state":"problem"}`,
			wantProjects: []string{"platform", "shop"},
			wantNoData:   "No Argo CD application that Sidekick may read matches the filter (name zzz*, state problem).",
		},
		{
			name:         "a name pattern that matches only what the config does not allow is no data",
			input:        `{"name":"invoices"}`,
			wantProjects: []string{"platform", "shop"},
			wantNoData:   "No Argo CD application that Sidekick may read matches the filter (name invoices).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, listPath, fixture("applications.json"))
			api.ExpectAllCalled()

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolListApplications, Input: json.RawMessage(tt.input)})
			require.NoError(t, err)

			reqs := api.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, "argocd", reqs[0].Query.Get("appNamespace"))
			require.Equal(t, tt.wantProjects, reqs[0].Query["projects"])
			require.Equal(t, "Bearer "+testToken, reqs[0].Header.Get("Authorization"))

			if tt.wantNoData != "" {
				require.True(t, res.NoData)
				require.Equal(t, tt.wantNoData, res.Text)
				require.Empty(t, res.Structured)

				return
			}

			require.False(t, res.NoData)
			require.JSONEq(t, withURL(tt.want, api.URL()), string(res.Structured))
			require.Equal(t, tt.wantEvidence(api.URL()), res.Evidence)
			require.Equal(t, tt.wantCut, res.Truncated)
		})
	}
}

func TestSourceListApplicationsProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		applications argo.Allowlist
		input        string
		wantProjects []string
		want         string
	}{
		{
			name:         "a glob with a wildcard project asks for every project",
			applications: argo.Allowlist{"*/ingress-*", "shop/cart"},
			input:        `{}`,
			want:         `{"total":2,"applications":[` + ingressJSON + `,` + cartJSON + `]}`,
		},
		{
			name:         "a project of a call is passed on when the globs have a wildcard project",
			applications: argo.Allowlist{"*/ingress-*", "shop/cart"},
			input:        `{"project":"platform"}`,
			wantProjects: []string{"platform"},
			want:         `{"total":1,"applications":[` + ingressJSON + `]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, listPath, fixture("applications.json"))
			api.ExpectAllCalled()

			s := newTestSourceFor(t, api, tokenSecrets(t), tt.applications)

			res, err := s.Call(t.Context(), source.Call{Tool: toolListApplications, Input: json.RawMessage(tt.input)})
			require.NoError(t, err)
			require.JSONEq(t, withURL(tt.want, api.URL()), string(res.Structured))
			require.Equal(t, tt.wantProjects, api.Requests()[0].Query["projects"])
		})
	}
}

func TestSourceCallInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{name: "a project the config does not allow", tool: toolListApplications, input: `{"project":"billing"}`,
			want: `project "billing" is not one Sidekick may read; allowed projects: platform, shop`},
		{name: "a project that is not a name", tool: toolListApplications, input: `{"project":"shop/../x"}`, want: `project: "shop/../x" is not a project name`},
		{name: "a name pattern that does not parse", tool: toolListApplications, input: `{"name":"[a"}`, want: `name: "[a" is not a valid pattern`},
		{name: "a state that does not exist", tool: toolListApplications, input: `{"state":"broken"}`,
			want: `state: "broken" must be any, out_of_sync, not_healthy or problem`},
		{name: "input that does not decode", tool: toolListApplications, input: `{"limit":"ten"}`, want: "the input does not decode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			s := newTestSource(t, api, registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			toolErr, ok := errors.AsType[*source.ToolError](err)
			require.True(t, ok, "want a tool error, got %v", err)
			require.Contains(t, toolErr.Message, tt.want)
			require.Empty(t, api.Requests(), "the call never reaches Argo CD")
		})
	}
}

func TestSourceCallFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		response      fakehttp.Response
		wantToolError string
		wantErr       string
	}{
		{
			name:          "a token that may not read is a tool error",
			response:      failed(http.StatusForbidden, "permission denied"),
			wantToolError: forbiddenText,
		},
		{
			name:          "a request Argo CD refuses is a tool error",
			response:      failed(http.StatusBadRequest, "invalid project"),
			wantToolError: "Argo CD answered 400: invalid project",
		},
		{
			name:     "a refused token is a failure of the source",
			response: failed(http.StatusUnauthorized, "invalid session: token is expired"),
			wantErr:  "Argo CD answered 401: invalid session: token is expired",
		},
		{
			name:     "a rate limit is a failure of the source",
			response: fakehttp.Response{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "10"}},
			wantErr:  "Argo CD answered 429",
		},
		{
			name:     "a server error is a failure of the source",
			response: failed(http.StatusInternalServerError, "boom"),
			wantErr:  "Argo CD answered 500: boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, listPath, tt.response)

			s := newTestSource(t, api, tokenSecrets(t))

			_, err := s.Call(t.Context(), source.Call{Tool: toolListApplications, Input: json.RawMessage(`{}`)})
			require.Error(t, err)
			require.NotContains(t, err.Error(), testToken)

			toolErr, isToolErr := errors.AsType[*source.ToolError](err)

			if tt.wantToolError != "" {
				require.True(t, isToolErr, "want a tool error, got %v", err)
				require.Equal(t, tt.wantToolError, toolErr.Message)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.False(t, isToolErr, "a failure of the source is not a tool error")
		})
	}
}

func TestSourceTokenFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an empty token secret", secret: []byte(" \n"), wantErr: "the token secret is empty"},
		{name: "a token secret that cannot be read", err: errors.New("no such file"), wantErr: "no such file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return(tt.secret, tt.err)

			api := fakehttp.New(t)
			s := newTestSource(t, api, secrets)

			_, err := s.Call(t.Context(), source.Call{Tool: toolListApplications, Input: json.RawMessage(`{}`)})
			require.ErrorContains(t, err, tt.wantErr)

			_, isToolErr := errors.AsType[*source.ToolError](err)
			require.False(t, isToolErr)
			require.Empty(t, api.Requests(), "no request is sent without a token")
		})
	}
}

func TestSourceUnknownTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tool string
	}{
		{name: "a tool that writes", tool: "sync_application"},
		{name: "an empty name", tool: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, fakehttp.New(t), registry.NewMockSecrets(gomock.NewController(t)))

			_, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(`{}`)})
			require.ErrorIs(t, err, errUnknownTool)
			require.NoError(t, s.Close())
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	cfg := Config{BaseURL: "https://argocd.example.com", TokenSecret: testTokenRef, Applications: testApplications}

	tests := []struct {
		name        string
		cfg         Config
		withSecrets bool
		wantErr     string
	}{
		{name: "without a secrets resolver", cfg: cfg, wantErr: errNoSecrets.Error()},
		{
			name: "with settings that do not check",
			cfg: Config{BaseURL: cfg.BaseURL, TokenSecret: testTokenRef, Applications: testApplications,
				Settings: source.Settings{Concurrency: -1}},
			withSecrets: true,
			wantErr:     "concurrency: must not be negative",
		},
		{name: "with a base URL on plain http", cfg: Config{BaseURL: "http://argocd.example.com", TokenSecret: testTokenRef, Applications: testApplications},
			withSecrets: true, wantErr: "must use https"},
		{name: "with the default HTTP client", cfg: cfg, withSecrets: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var deps registry.Deps
			if tt.withSecrets {
				deps.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			built, err := New(t.Context(), "argocd", tt.cfg, deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			tools, err := built.Tools(t.Context())
			require.NoError(t, err)
			require.Len(t, tools, len(toolList))
			require.NoError(t, built.Close())
		})
	}
}

func TestSourceListApplicationsTextIsCut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		long string
	}{
		{name: "a revision that is too long", long: strings.Repeat("é", maxNameLen)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, listPath, body(`{"items":[{"metadata":{"name":"cart","namespace":"argocd"},"spec":{"project":"shop"},
				"status":{"sync":{"status":"Synced","revision":"`+tt.long+`"}}}]}`))

			s := newTestSource(t, api, tokenSecrets(t))

			res, err := s.Call(t.Context(), source.Call{Tool: toolListApplications, Input: json.RawMessage(`{}`)})
			require.NoError(t, err)

			var got applicationList
			require.NoError(t, json.Unmarshal(res.Structured, &got))
			require.Len(t, got.Applications, 1)
			require.LessOrEqual(t, len(got.Applications[0].ComparedRevision), maxNameLen)
			require.Equal(t, "Unknown", got.Applications[0].Health)
		})
	}
}

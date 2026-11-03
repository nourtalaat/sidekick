package argocd

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testToken is the API token the test secret holds.
const testToken = "argocd-00000000-test-token"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-argocd", Key: "token"}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// tokenSecrets returns a secrets resolver that serves the test token,
// followed by a newline as a file holds it, any number of times.
func tokenSecrets(t *testing.T) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	return s
}

// newTestClient returns a client of the fake server api.
func newTestClient(t *testing.T, api *fakehttp.Server, secrets registry.Secrets) *Client {
	t.Helper()

	c, err := New(Options{BaseURL: api.URL(), TokenSecret: testTokenRef, HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	return c
}

// failed returns an Argo CD error answer.
func failed(status int, message string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: []byte(`{"error":"` + message + `","code":7,"message":"` + message + `"}`)}
}

func TestClientGetApplication(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      AppRef
		response fakehttp.Response
		wantCode int
		wantMsg  string
		wantErr  string
	}{
		{
			name:     "an application of the namespace",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: fakehttp.Response{Headers: jsonHeaders, BodyFile: "fixture/application.json"},
		},
		{
			name:     "an application that does not exist",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: failed(http.StatusNotFound, `applications.argoproj.io \"checkout\" not found`),
			wantCode: http.StatusNotFound,
			wantMsg:  `applications.argoproj.io "checkout" not found`,
		},
		{
			name:     "a token that may not get the application",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: failed(http.StatusForbidden, "permission denied"),
			wantCode: http.StatusForbidden,
			wantMsg:  "permission denied",
		},
		{
			name:     "a refused token",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: failed(http.StatusUnauthorized, "invalid session: token is expired"),
			wantCode: http.StatusUnauthorized,
			wantMsg:  "invalid session: token is expired",
		},
		{
			name:     "an error answer that is not JSON",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: fakehttp.Response{Status: http.StatusBadGateway, Body: []byte("<html>bad gateway</html>")},
			wantCode: http.StatusBadGateway,
		},
		{
			name:     "an application of another namespace",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"metadata":{"name":"checkout","namespace":"team-a"},"spec":{"project":"shop"}}`)},
			wantErr:  "not in the namespace",
		},
		{
			name:     "an answer that is not an application",
			ref:      AppRef{Project: "shop", Name: "checkout"},
			response: fakehttp.Response{Headers: jsonHeaders, Body: []byte(`[1]`)},
			wantErr:  "decode the answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v1/applications/checkout", tt.response)
			api.ExpectAllCalled()

			c := newTestClient(t, api, tokenSecrets(t))

			app, err := c.GetApplication(t.Context(), tt.ref)

			reqs := api.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, "argocd", reqs[0].Query.Get("appNamespace"))
			require.Equal(t, "shop", reqs[0].Query.Get("project"))
			require.Equal(t, "Bearer "+testToken, reqs[0].Header.Get("Authorization"))
			require.Equal(t, "application/json", reqs[0].Header.Get("Accept"))
			require.Contains(t, reqs[0].Header.Get("User-Agent"), "sidekick/")

			switch {
			case tt.wantCode != 0:
				se, ok := errors.AsType[*StatusError](err)
				require.True(t, ok, "got %v", err)
				require.Equal(t, tt.wantCode, se.Code)
				require.Equal(t, tt.wantMsg, se.Message)
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, AppRef{Project: "shop", Name: "checkout"}, app.Ref())
				require.Len(t, app.Status.History, 3)
			}
		})
	}
}

func TestClientListApplications(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		query        ListQuery
		wantProjects []string
		wantApps     []AppRef
	}{
		{
			name:     "every project, without the application of another namespace",
			wantApps: []AppRef{{Project: "shop", Name: "checkout"}, {Project: "default", Name: "ingress"}},
		},
		{
			name:         "some projects",
			query:        ListQuery{Projects: []string{"shop", "platform"}},
			wantProjects: []string{"shop", "platform"},
			wantApps:     []AppRef{{Project: "shop", Name: "checkout"}, {Project: "default", Name: "ingress"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, "/api/v1/applications", fakehttp.Response{Headers: jsonHeaders, BodyFile: "fixture/applications.json"})
			api.ExpectAllCalled()

			c := newTestClient(t, api, tokenSecrets(t))

			apps, err := c.ListApplications(t.Context(), tt.query)
			require.NoError(t, err)

			reqs := api.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, "argocd", reqs[0].Query.Get("appNamespace"))
			require.Equal(t, tt.wantProjects, reqs[0].Query["projects"])

			got := make([]AppRef, 0, len(apps))
			for i := range apps {
				got = append(got, apps[i].Ref())
			}

			require.Equal(t, tt.wantApps, got)
		})
	}
}

func TestClientRevisionMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		revision string
		// route is the decoded path the fake serves, and escaped the path as
		// the client sends it.
		route    string
		escaped  string
		response fakehttp.Response
		want     RevisionMetadata
		wantCode int
	}{
		{
			name:     "a commit",
			revision: "cccc3333",
			route:    "/api/v1/applications/checkout/revisions/cccc3333/metadata",
			escaped:  "/api/v1/applications/checkout/revisions/cccc3333/metadata",
			response: fakehttp.Response{Headers: jsonHeaders, Body: []byte(
				`{"author":"Ada <ada@example.com>","date":"2026-11-03T08:00:00Z","tags":["v1.4.2"],"message":"Raise the pool size"}`)},
			want: RevisionMetadata{
				Author:  "Ada <ada@example.com>",
				Date:    new(time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)),
				Tags:    []string{"v1.4.2"},
				Message: "Raise the pool size",
			},
		},
		{
			name:     "a chart version is escaped into the path",
			revision: "1.2.3+build/4",
			route:    "/api/v1/applications/checkout/revisions/1.2.3+build/4/metadata",
			escaped:  "/api/v1/applications/checkout/revisions/1.2.3+build%2F4/metadata",
			response: fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{}`)},
		},
		{
			name:     "a revision the repository does not have",
			revision: "dead",
			route:    "/api/v1/applications/checkout/revisions/dead/metadata",
			escaped:  "/api/v1/applications/checkout/revisions/dead/metadata",
			response: failed(http.StatusInternalServerError, "revision not found"),
			wantCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			escaped := make(chan string, 1)

			api.HandleFunc(http.MethodGet, tt.route, func(w http.ResponseWriter, r *http.Request) {
				escaped <- r.URL.EscapedPath()

				for k, v := range tt.response.Headers {
					w.Header().Set(k, v)
				}

				w.WriteHeader(max(tt.response.Status, http.StatusOK))
				_, _ = w.Write(tt.response.Body)
			})

			c := newTestClient(t, api, tokenSecrets(t))

			meta, err := c.RevisionMetadata(t.Context(), AppRef{Project: "shop", Name: "checkout"}, tt.revision, 6)

			require.Equal(t, tt.escaped, <-escaped)

			reqs := api.Requests()
			require.Len(t, reqs, 1)
			require.Equal(t, "6", reqs[0].Query.Get("versionId"))
			require.Equal(t, "shop", reqs[0].Query.Get("project"))
			require.Equal(t, "argocd", reqs[0].Query.Get("appNamespace"))

			if tt.wantCode != 0 {
				se, ok := errors.AsType[*StatusError](err)
				require.True(t, ok, "got %v", err)
				require.Equal(t, tt.wantCode, se.Code)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, *meta)
		})
	}
}

func TestClientRequests(t *testing.T) {
	t.Parallel()

	t.Run("the base URL may hold a sub-path", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodGet, "/tools/argocd/api/v1/applications", fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"items":[]}`)})
		api.ExpectAllCalled()

		c, err := New(Options{BaseURL: api.URL() + "/tools/argocd/", TokenSecret: testTokenRef, HTTP: api.Client(), Secrets: tokenSecrets(t)})
		require.NoError(t, err)

		_, err = c.ListApplications(t.Context(), ListQuery{})
		require.NoError(t, err)
		require.Equal(t, api.URL()+"/tools/argocd/applications/argocd/checkout", c.ApplicationURL(AppRef{Project: "shop", Name: "checkout"}))
	})

	t.Run("the namespace may be set", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodGet, "/api/v1/applications/checkout", fakehttp.Response{Headers: jsonHeaders,
			Body: []byte(`{"metadata":{"name":"checkout","namespace":"argo-cd"},"spec":{"project":"shop"}}`)})
		api.ExpectAllCalled()

		c, err := New(Options{BaseURL: api.URL(), TokenSecret: testTokenRef, Namespace: "argo-cd", HTTP: api.Client(), Secrets: tokenSecrets(t)})
		require.NoError(t, err)

		_, err = c.GetApplication(t.Context(), AppRef{Project: "shop", Name: "checkout"})
		require.NoError(t, err)
		require.Equal(t, "argo-cd", api.Requests()[0].Query.Get("appNamespace"))
		require.Equal(t, api.URL()+"/applications/argo-cd/checkout", c.ApplicationURL(AppRef{Project: "shop", Name: "checkout"}))
	})

	t.Run("the token is read on every request", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodGet, "/api/v1/applications", fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"items":[]}`)},
			fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"items":[]}`)})
		api.ExpectAllCalled()

		secrets := registry.NewMockSecrets(gomock.NewController(t))
		gomock.InOrder(
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte("first"), nil),
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte("second"), nil),
		)

		c := newTestClient(t, api, secrets)

		for range 2 {
			_, err := c.ListApplications(t.Context(), ListQuery{})
			require.NoError(t, err)
		}

		reqs := api.Requests()
		require.Equal(t, "Bearer first", reqs[0].Header.Get("Authorization"))
		require.Equal(t, "Bearer second", reqs[1].Header.Get("Authorization"))
	})

	t.Run("an empty token sends nothing", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)

		secrets := registry.NewMockSecrets(gomock.NewController(t))
		secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(" \n"), nil)

		_, err := newTestClient(t, api, secrets).ListApplications(t.Context(), ListQuery{})
		require.ErrorIs(t, err, ErrEmptyToken)
		require.Empty(t, api.Requests())
	})

	t.Run("a secret that cannot be read sends nothing", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		errRead := errors.New("no such file")

		secrets := registry.NewMockSecrets(gomock.NewController(t))
		secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return(nil, errRead)

		_, err := newTestClient(t, api, secrets).ListApplications(t.Context(), ListQuery{})
		require.ErrorIs(t, err, errRead)
		require.Empty(t, api.Requests())
	})

	t.Run("an answer over the size limit fails", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodGet, "/api/v1/applications", fakehttp.Response{Headers: jsonHeaders, Body: []byte(`{"items":[],"pad":"0123456789"}`)})

		c := newTestClient(t, api, tokenSecrets(t))
		c.maxResponse = 16

		_, err := c.ListApplications(t.Context(), ListQuery{})
		require.ErrorIs(t, err, errResponseTooLarge)
	})
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		secrets bool
		wantErr string
	}{
		{name: "https", opts: Options{BaseURL: "https://argocd.example.com"}, secrets: true},
		{name: "https with a sub-path and a slash", opts: Options{BaseURL: "https://example.com/argocd/"}, secrets: true},
		{name: "http on loopback", opts: Options{BaseURL: "http://127.0.0.1:8080"}, secrets: true},
		{name: "http on localhost", opts: Options{BaseURL: "http://localhost:8080"}, secrets: true},
		{name: "http elsewhere", opts: Options{BaseURL: "http://argocd.example.com"}, secrets: true, wantErr: "must use https"},
		{name: "no base URL", opts: Options{}, secrets: true, wantErr: "baseURL: required"},
		{name: "no secrets", opts: Options{BaseURL: "https://argocd.example.com"}, wantErr: "no secrets resolver"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.secrets {
				tt.opts.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			c, err := New(tt.opts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, DefaultNamespace, c.namespace)
			require.NotEmpty(t, c.userAgent)
			require.NotNil(t, c.http)
			require.NotContains(t, c.baseURL[len(c.baseURL)-1:], "/")
		})
	}
}

func TestCheckBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{name: "https", url: "https://argocd.example.com"},
		{name: "https with a port and a path", url: "https://argocd.example.com:8443/cd"},
		{name: "http on IPv6 loopback", url: "http://[::1]:8080"},
		{name: "http on a loopback name that is not localhost", url: "http://argocd.localhost", wantErr: "must use https"},
		{name: "http on a private address", url: "http://10.0.0.5", wantErr: "must use https"},
		{name: "ftp", url: "ftp://argocd.example.com", wantErr: "absolute https URL"},
		{name: "no scheme", url: "argocd.example.com", wantErr: "absolute https URL"},
		{name: "a query", url: "https://argocd.example.com?x=1", wantErr: "absolute https URL"},
		{name: "a fragment", url: "https://argocd.example.com#x", wantErr: "absolute https URL"},
		{name: "user info", url: "https://admin@argocd.example.com", wantErr: "absolute https URL"},
		{name: "no host", url: "https://", wantErr: "absolute https URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckBaseURL(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorContains(t, err, "baseURL")
		})
	}
}

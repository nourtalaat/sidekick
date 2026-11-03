package gitlabapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestNextPage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header http.Header
		want   int
	}{
		{name: "no paging headers", header: http.Header{}, want: 0},
		{name: "x-next-page", header: http.Header{"X-Next-Page": {"3"}}, want: 3},
		{name: "an empty x-next-page on the last page", header: http.Header{"X-Next-Page": {""}}, want: 0},
		{name: "an x-next-page that is not a number", header: http.Header{"X-Next-Page": {"soon"}}, want: 0},
		{
			name: "a next link, whatever its host",
			header: http.Header{"Link": {
				`<https://other.example.net/api/v4/projects/1/repository/commits?page=1&per_page=20>; rel="first", ` +
					`<https://other.example.net/api/v4/projects/1/repository/commits?page=4&per_page=20>; rel="next"`,
			}},
			want: 4,
		},
		{
			name: "links in separate headers",
			header: http.Header{"Link": {
				`<https://gitlab.example.com/a?page=1>; rel="prev"`, `<https://gitlab.example.com/a?page=3>; rel="next"`,
			}},
			want: 3,
		},
		{name: "no next link", header: http.Header{"Link": {`<https://gitlab.example.com/a?page=1>; rel="prev"`}}, want: 0},
		{name: "a next link without a page", header: http.Header{"Link": {`<https://gitlab.example.com/a?cursor=abc>; rel="next"`}}, want: 0},
		{name: "x-next-page wins over the link", header: http.Header{"X-Next-Page": {"2"}, "Link": {`<https://x.example.com/a?page=9>; rel="next"`}}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, nextPage(tt.header))
		})
	}
}

func TestProjectPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		project string
		parts   []string
		want    string
	}{
		{name: "a project in a group", project: "acme/checkout", parts: []string{"repository", "commits"}, want: "/projects/acme%2Fcheckout/repository/commits"},
		{
			name:    "a project in subgroups and a file path",
			project: "acme/platform/team/checkout",
			parts:   []string{"repository", "files", url.PathEscape("src/main app.go")},
			want:    "/projects/acme%2Fplatform%2Fteam%2Fcheckout/repository/files/src%2Fmain%20app.go",
		},
		{name: "a project name that tries to leave its place", project: "a/b?c=d#e", parts: []string{"x"}, want: "/projects/a%2Fb%3Fc=d%23e/x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ProjectPath(tt.project, tt.parts...))
		})
	}
}

func TestClientRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		// wantRaw is the path as it must reach GitLab.
		wantRaw string
		// route is the decoded path the fake server matches.
		route string
	}{
		{
			name:    "a project in a subgroup",
			path:    ProjectPath("acme/platform/checkout", "repository", "commits"),
			wantRaw: "/api/v4/projects/acme%2Fplatform%2Fcheckout/repository/commits",
			route:   "/api/v4/projects/acme/platform/checkout/repository/commits",
		},
		{
			name:    "a file path with slashes and a space",
			path:    ProjectPath("acme/checkout", "repository", "files", url.PathEscape("deploy/my values.yaml")),
			wantRaw: "/api/v4/projects/acme%2Fcheckout/repository/files/deploy%2Fmy%20values.yaml",
			route:   "/api/v4/projects/acme/checkout/repository/files/deploy/my values.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := make(chan string, 1)

			api := fakehttp.New(t)
			api.HandleFunc(http.MethodGet, tt.route, func(w http.ResponseWriter, r *http.Request) {
				raw <- r.URL.EscapedPath()

				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			})

			c := newTestClient(t, api, tokenSecrets(t))

			var out map[string]any

			_, err := c.Get(t.Context(), tt.path, nil, &out)
			require.NoError(t, err)
			require.Equal(t, tt.wantRaw, <-raw)

			requests := api.Requests()
			require.Len(t, requests, 1)
			require.Equal(t, testToken, requests[0].Header.Get("PRIVATE-TOKEN"))
			require.Empty(t, requests[0].Header.Get("Authorization"))
			require.Equal(t, "application/json", requests[0].Header.Get("Accept"))
			require.True(t, strings.HasPrefix(requests[0].Header.Get("User-Agent"), "sidekick/"))
		})
	}
}

func TestClientReadsTheTokenOnEveryCall(t *testing.T) {
	t.Parallel()

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	gomock.InOrder(
		secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte("glpat-old\n"), nil),
		secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte("glpat-rotated\n"), nil),
	)

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, apiRoot+"/version", body(`{}`), body(`{}`))
	api.ExpectAllCalled()

	c := newTestClient(t, api, secrets)

	var out map[string]any

	_, err := c.Get(t.Context(), "/version", nil, &out)
	require.NoError(t, err)

	_, err = c.Get(t.Context(), "/version", nil, &out)
	require.NoError(t, err)

	requests := api.Requests()
	require.Len(t, requests, 2)
	require.Equal(t, "glpat-old", requests[0].Header.Get("PRIVATE-TOKEN"))
	require.Equal(t, "glpat-rotated", requests[1].Header.Get("PRIVATE-TOKEN"))
}

func TestClientTokenErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		secret  []byte
		err     error
		wantErr string
	}{
		{name: "an empty token", secret: []byte(" \n"), wantErr: ErrEmptyToken.Error()},
		{name: "a secret that cannot be read", err: errNoSecrets, wantErr: "resolve secret sidekick-gitlab/token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			secrets := registry.NewMockSecrets(gomock.NewController(t))
			secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return(tt.secret, tt.err)

			api := fakehttp.New(t)
			c := newTestClient(t, api, secrets)

			var out map[string]any

			_, err := c.Get(t.Context(), "/version", nil, &out)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, api.Requests(), "no request goes out without a token")
		})
	}
}

func TestClientRedirects(t *testing.T) {
	t.Parallel()

	t.Run("a moved project is followed on the same host", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.HandleFunc(http.MethodGet, apiRoot+"/projects/81", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":81}`))
		})
		api.HandleFunc(http.MethodGet, apiRoot+"/projects/acme/moved", func(w http.ResponseWriter, r *http.Request) {
			movedTo(w, "http://"+r.Host+apiRoot+"/projects/81")
		})

		c := newTestClient(t, api, tokenSecrets(t))

		var out struct {
			ID int `json:"id"`
		}

		_, err := c.Get(t.Context(), ProjectPath("acme/moved"), nil, &out)
		require.NoError(t, err)
		require.Equal(t, 81, out.ID)
	})

	t.Run("a redirect to another host is refused", func(t *testing.T) {
		t.Parallel()

		other := fakehttp.New(t)

		api := fakehttp.New(t)
		api.HandleFunc(http.MethodGet, apiRoot+"/projects/acme/moved", func(w http.ResponseWriter, _ *http.Request) {
			movedTo(w, other.URL()+apiRoot+"/projects/81")
		})

		c := newTestClient(t, api, tokenSecrets(t))

		var out map[string]any

		_, err := c.Get(t.Context(), ProjectPath("acme/moved"), nil, &out)
		require.ErrorIs(t, err, ErrRedirect)
		require.Empty(t, other.Requests(), "the token never goes to the other host")
	})

	t.Run("a redirect loop is stopped", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.HandleFunc(http.MethodGet, apiRoot+"/projects/acme/loop", func(w http.ResponseWriter, r *http.Request) {
			movedTo(w, "http://"+r.Host+apiRoot+"/projects/acme/loop")
		})

		c := newTestClient(t, api, tokenSecrets(t))

		var out map[string]any

		_, err := c.Get(t.Context(), ProjectPath("acme/loop"), nil, &out)
		require.ErrorIs(t, err, ErrRedirect)
		require.Len(t, api.Requests(), maxRedirects+1)
	})
}

func TestClientResponseLimit(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, apiRoot+"/big", body(`{"text":"`+strings.Repeat("x", 100)+`"}`))

	c := newTestClient(t, api, tokenSecrets(t))

	var out map[string]any

	_, err := c.GetLimited(t.Context(), "/big", nil, &out, 50)
	require.ErrorIs(t, err, ErrResponseTooLarge)
}

// movedTo answers a request with a permanent redirect to location.
func movedTo(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusMovedPermanently)
}

func TestClientSend(t *testing.T) {
	t.Parallel()

	t.Run("a JSON body and a JSON answer", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodPost, apiRoot+"/projects/acme/checkout/repository/commits", fakehttp.Response{
			Status: http.StatusCreated, Headers: jsonHeaders, Body: []byte(`{"id":"abc"}`),
		})
		api.ExpectAllCalled()

		c := newTestClient(t, api, tokenSecrets(t))

		var out struct {
			ID string `json:"id"`
		}

		err := c.Send(t.Context(), http.MethodPost, ProjectPath("acme/checkout", "repository", "commits"), map[string]string{"branch": "b"}, &out)
		require.NoError(t, err)
		require.Equal(t, "abc", out.ID)

		requests := api.Requests()
		require.Len(t, requests, 1)
		require.JSONEq(t, `{"branch":"b"}`, string(requests[0].Body))
		require.Equal(t, "application/json", requests[0].Header.Get("Content-Type"))
		require.Equal(t, testToken, requests[0].Header.Get("PRIVATE-TOKEN"))
	})

	t.Run("no body and an answer with no content", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodDelete, apiRoot+"/projects/acme/checkout/repository/branches/sidekick/x", fakehttp.Response{Status: http.StatusNoContent})
		api.ExpectAllCalled()

		c := newTestClient(t, api, tokenSecrets(t))

		err := c.Send(t.Context(), http.MethodDelete, ProjectPath("acme/checkout", "repository", "branches", "sidekick%2Fx"), nil, nil)
		require.NoError(t, err)

		requests := api.Requests()
		require.Len(t, requests, 1)
		require.Empty(t, requests[0].Body)
		require.Empty(t, requests[0].Header.Get("Content-Type"))
	})

	t.Run("an error answer carries GitLab's message", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodPost, apiRoot+"/projects/acme/checkout/merge_requests", fakehttp.Response{
			Status: http.StatusConflict, Headers: jsonHeaders, Body: []byte(`{"message":["Another open merge request already exists"]}`),
		})

		c := newTestClient(t, api, tokenSecrets(t))

		err := c.Send(t.Context(), http.MethodPost, ProjectPath("acme/checkout", "merge_requests"), map[string]string{"title": "t"}, nil)
		require.Error(t, err)
		require.True(t, IsStatus(err, http.StatusConflict))
		require.ErrorContains(t, err, "Another open merge request already exists")
	})

	t.Run("a redirect is never followed", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.HandleFunc(http.MethodPost, apiRoot+"/projects/acme/moved/merge_requests", func(w http.ResponseWriter, r *http.Request) {
			movedTo(w, "http://"+r.Host+apiRoot+"/projects/81/merge_requests")
		})

		c := newTestClient(t, api, tokenSecrets(t))

		err := c.Send(t.Context(), http.MethodPost, ProjectPath("acme/moved", "merge_requests"), map[string]string{"title": "t"}, nil)
		require.True(t, IsStatus(err, http.StatusMovedPermanently))
		require.Len(t, api.Requests(), 1, "the redirect is not followed, so nothing is sent to the new address")
	})

	t.Run("no request goes out without a token", func(t *testing.T) {
		t.Parallel()

		secrets := registry.NewMockSecrets(gomock.NewController(t))
		secrets.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(" \n"), nil)

		api := fakehttp.New(t)
		c := newTestClient(t, api, secrets)

		err := c.Send(t.Context(), http.MethodPut, "/projects/1/merge_requests/2", map[string]string{"state_event": "close"}, nil)
		require.ErrorIs(t, err, ErrEmptyToken)
		require.Empty(t, api.Requests())
	})
}

package githubapp

import (
	"net/http"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestAppCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		app  App
		want []string
	}{
		{name: "a whole app", app: testApp},
		{name: "an empty app", app: App{}, want: []string{"app.appID: required", "app.installationID: required", "app.privateKeySecret: required"}},
		{name: "a negative app ID", app: App{AppID: -1, InstallationID: 1, PrivateKeySecret: testKeyRef}, want: []string{"app.appID: required"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got []string
			for _, err := range tt.app.Check("app") {
				got = append(got, err.Error())
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestPermissions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		got  *gh.InstallationPermissions
		want string
	}{
		{name: "read only", got: ReadOnly(), want: `{"actions":"read","contents":"read","metadata":"read","pull_requests":"read"}`},
		{name: "read and write", got: ReadWrite(), want: `{"contents":"write","metadata":"read","pull_requests":"write"}`},
		{name: "checks read", got: ChecksRead(), want: `{"checks":"read","metadata":"read"}`},
		{name: "commit statuses read", got: StatusesRead(), want: `{"metadata":"read","statuses":"read"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, testExchangePath, tokenResponse("ghs_one", testStart.Add(time.Hour)))
			api.ExpectAllCalled()

			tokens := newTestTokens(api, keySecrets(t, 1), func() time.Time { return testStart })
			tokens.permissions = tt.got

			_, err := tokens.Token(t.Context())
			require.NoError(t, err)
			require.JSONEq(t, `{"permissions":`+tt.want+`}`, string(api.Requests()[0].Body))
		})
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("sends requests with an installation token", func(t *testing.T) {
		t.Parallel()

		api := fakehttp.New(t)
		api.Handle(http.MethodPost, testExchangePath, tokenResponse("ghs_one", testStart.Add(time.Hour)))
		api.Handle(http.MethodGet, "/repos/acme/checkout", fakehttp.Response{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"full_name":"acme/checkout"}`),
		})
		api.ExpectAllCalled()

		client, err := NewClient(Options{
			App:         testApp,
			BaseURL:     api.URL() + "/",
			Secrets:     keySecrets(t, 1),
			HTTP:        api.Client(),
			UserAgent:   "sidekick-test",
			Permissions: ReadWrite(),
			Now:         func() time.Time { return testStart },
		})
		require.NoError(t, err)

		repo, _, err := client.Repositories.Get(t.Context(), "acme", "checkout")
		require.NoError(t, err)
		require.Equal(t, "acme/checkout", repo.GetFullName())

		reqs := api.Requests()
		require.Equal(t, "Bearer ghs_one", reqs[1].Header.Get("Authorization"))
		require.Equal(t, "sidekick-test", reqs[1].Header.Get("User-Agent"))
		require.JSONEq(t, `{"permissions":{"contents":"write","metadata":"read","pull_requests":"write"}}`, string(reqs[0].Body))
	})

	t.Run("needs a secrets resolver", func(t *testing.T) {
		t.Parallel()

		_, err := NewClient(Options{App: testApp, BaseURL: "https://api.github.com/", Permissions: ReadOnly()})
		require.ErrorIs(t, err, ErrNoSecrets)
	})

	t.Run("refuses a base URL that does not parse", func(t *testing.T) {
		t.Parallel()

		_, err := NewClient(Options{App: testApp, BaseURL: "://bad", Secrets: keySecrets(t, 0), Permissions: ReadOnly()})
		require.ErrorContains(t, err, "parse the base URL")
	})
}

//go:build integration

package slackroles_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The responses of usergroups.users.list in fixture/slack.
const (
	membersFile = "fixture/slack/members.json"
	noneFile    = "fixture/slack/no-members.json"
	errorFile   = "fixture/slack/not-allowed.json"
)

// listPath is the path of usergroups.users.list on the fake Slack.
const listPath = "/api/usergroups.users.list"

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a Lookup on a database of its own, seeded with
// fixture/identities.sql, that calls a fake Slack.
func setup(t *testing.T) (*slackroles.Lookup, *fakehttp.Server, *pgxpool.Pool) {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/identities.sql")

	api := fakehttp.New(t)

	cfg := slackConfig()
	cfg.BotTokenSecret = registry.SecretRef{Name: "sidekick-slack", Key: "botToken"}
	cfg.APIURL = api.URL() + "/api/"

	lookup, err := slackroles.New(slackroles.Options{Slack: cfg, Secrets: staticSecrets{}, Pool: pool, HTTP: api.Client()})
	require.NoError(t, err)

	return lookup, api, pool
}

// staticSecrets resolves every reference to the same fake token.
type staticSecrets struct{}

func (staticSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("xoxb-fake\n"), nil
}

// groupCalls returns the user groups the fake Slack was asked about, in
// order.
func groupCalls(t *testing.T, api *fakehttp.Server) []string {
	t.Helper()

	var groups []string

	for _, req := range api.Requests() {
		require.Equal(t, http.MethodPost, req.Method)
		require.Equal(t, listPath, req.Path)

		values, err := url.ParseQuery(string(req.Body))
		require.NoError(t, err)

		groups = append(groups, values.Get("usergroup"))
	}

	return groups
}

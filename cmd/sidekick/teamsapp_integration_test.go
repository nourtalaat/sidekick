//go:build integration

package main

import (
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestServeTeamsBot checks that `sidekick serve` with a teams section
// serves the bot's messaging endpoint at /teams/messages on server.listen,
// and refuses an activity without a Bot Framework token before reading it.
func TestServeTeamsBot(t *testing.T) {
	t.Parallel()

	anthropic := fakeAnthropic(t)

	content := serveConfig(anthropic.URL()) + `
teams:
  appID: 00000000-1111-2222-3333-444444444444
  tenantID: 55555555-6666-7777-8888-999999999999
  clientSecret: { name: teams, key: clientSecret }
`

	cmd, lines, started := startServe(t, writeConfig(t, content, storetest.NewURL(t), "teams/clientSecret"))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+started.Listen+"/teams/messages", strings.NewReader(`{"type": "message"}`))
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	stopServe(t, cmd, lines, syscall.SIGTERM)
}

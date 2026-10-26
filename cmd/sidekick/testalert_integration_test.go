//go:build integration

package main

import (
	"bytes"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestTestAlertWithServe sends the test alert to a running `sidekick serve`
// and follows it to its investigation, so the payload and the bearer token
// are what the real endpoint accepts.
func TestTestAlertWithServe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		wantOut   string
		wantRoute string
	}{
		{name: "the alert is accepted and investigated", wantOut: "Sidekick answered 202 Accepted", wantRoute: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			anthropic := fakeAnthropic(t, "fixture/anthropic-inconclusive.sse")
			configPath := writeConfig(t, webhookConfig(anthropic.URL()), storetest.NewURL(t), "hooks/alertmanager")

			cmd, lines, started := startServe(t, configPath)

			var out bytes.Buffer

			// The config names the real anthropic provider, with baseURL.
			root := newTestRootCmd(newTestAlertCmd(config.Registries{}))
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"test-alert", "-c", configPath, "--url", "http://" + started.Listen})

			require.NoError(t, root.Execute())
			require.Contains(t, out.String(), tt.wantOut)

			require.Equal(t, tt.wantRoute, waitForLog(t, lines, "event routed").Route)
			require.Equal(t, "inconclusive", waitForLog(t, lines, "investigation finished").Verdict)

			stopServe(t, cmd, lines, syscall.SIGTERM)
		})
	}
}

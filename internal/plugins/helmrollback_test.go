package plugins_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
)

const helmRollbackConfig = `
database:
  urlSecret: { name: sidekick-db, key: url }
model:
  connectors:
    claude: { provider: anthropic, model: claude-opus-5-5 }
  roles:
    investigate: claude
intake:
  webhooks:
    alertmanager:
      adapter: alertmanager
      auth: { bearer: { name: sidekick-hooks, key: alertmanager } }
      trusted: true
actions:
  helm-web:
    type: helm.rollback
    ALLOW
    limits:
      namespaces: [shop]
      releases: [web]
routes:
  - { name: ops, match: { intake: [alertmanager] }, mode: MODE, actions: [helm-web] }
  - { name: default }
`

func TestHelmRollbackOnReadWriteRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allow   string
		mode    string
		wantErr string
	}{
		{
			name: "read-write without the opt-in", allow: "", mode: "read-write",
			wantErr: `routes[0].actions[0]: action "helm-web" (type helm.rollback) is approval-only, so a read-write route may not list it as it is; ` +
				"use mode: approved on this route, or set allowReadWrite: true on the action in the config file",
		},
		{name: "read-write with the opt-in", allow: "allowReadWrite: true", mode: "read-write"},
		{name: "approved without the opt-in", allow: "", mode: "approved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			yaml := strings.NewReplacer("ALLOW", tt.allow, "MODE", tt.mode).Replace(helmRollbackConfig)

			_, err := config.Parse(t.Context(), []byte(yaml), config.LoadOptions{})
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

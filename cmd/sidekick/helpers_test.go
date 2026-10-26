package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// writeConfig writes content, with SECRETS_DIR replaced, to a sidekick.yaml
// of its own and returns its path. The secrets directory holds the database
// URL dbURL, unless it is empty, a fake Anthropic API key, and a fake value
// for each of the extra secret paths, such as "hooks/alertmanager".
func writeConfig(t *testing.T, content, dbURL string, extra ...string) string {
	t.Helper()

	secretsDir := writeSecrets(t, append([]string{"anthropic/apiKey"}, extra...))
	if dbURL != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(secretsDir, "sidekick-db"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "sidekick-db", "url"), []byte(dbURL+"\n"), 0o600))
	}

	path := filepath.Join(t.TempDir(), "sidekick.yaml")
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(content, "SECRETS_DIR", secretsDir)), 0o600))

	return path
}

// fakeRegistries returns registries that hold only the fake anthropic
// provider.
func fakeRegistries() config.Registries {
	regs := config.Registries{Models: registry.New[model.Connector]("model")}
	registerFakes(regs.Models)

	return regs
}

// newTestRootCmd builds a root command holding only cmd.
func newTestRootCmd(cmd *cobra.Command) *cobra.Command {
	root := &cobra.Command{Use: "sidekick", SilenceUsage: true}
	root.AddCommand(cmd)

	return root
}

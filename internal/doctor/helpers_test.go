package doctor

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	_ "github.com/nourtalaat/sidekick/internal/plugins" // Registers the built-in plugin types.
)

// Fake secret values. No test sends them to a real service.
const (
	testDBPassword = "db-password-not-real" //nolint:gosec // A fake value.
	testBotToken   = "xoxb-test-not-real"   //nolint:gosec // A fake value.
	testAPIKey     = "sk-ant-test-not-real" //nolint:gosec // A fake value.
	testDBURL      = "postgres://sidekick:" + testDBPassword + "@db.example:5432/sidekick"
)

// baseConfig is a valid config with one Anthropic connector at
// ANTHROPIC_URL, and secrets in SECRETS_DIR.
const baseConfig = `
secrets: { dir: "SECRETS_DIR" }
database:
  urlSecret: { name: sidekick-db, key: url }
model:
  connectors:
    claude: { provider: anthropic, model: claude-opus-5-5, apiKeySecret: { name: anthropic, key: apiKey }, baseURL: "ANTHROPIC_URL" }
  roles:
    investigate: claude
routes:
  - name: default
`

// localConfig is a valid config with the Anthropic connector of baseConfig
// and an OpenAI-compatible connector "local" at LOCAL_URL/v1, running
// LOCAL_MODEL with LOCAL_EXTRA appended to its settings. INVESTIGATE fills
// the investigate role of model.roles, and ROUTE_ROLES is the roles block
// of the default route.
const localConfig = `
secrets: { dir: "SECRETS_DIR" }
database:
  urlSecret: { name: sidekick-db, key: url }
model:
  connectors:
    claude: { provider: anthropic, model: claude-opus-5-5, apiKeySecret: { name: anthropic, key: apiKey }, baseURL: "ANTHROPIC_URL" }
    local: { provider: openai, model: "LOCAL_MODEL", baseURL: "LOCAL_URL/v1"LOCAL_EXTRA }
  roles:
    investigate: INVESTIGATE
routes:
  - name: defaultROUTE_ROLES
`

// localReplacements fills the placeholders of localConfig, with
// replacements given in pairs winning over the defaults: the Qwen3.8 preset
// as an Ollama tag, no extra settings, and local investigating.
func localReplacements(pairs ...string) []string {
	return slices.Concat(pairs, []string{
		"LOCAL_MODEL", "qwen3.8:27b",
		"LOCAL_URL", "http://127.0.0.1:1",
		"LOCAL_EXTRA", "",
		"INVESTIGATE", "local",
		"ROUTE_ROLES", "",
	})
}

// defaultSecrets are the secret files most tests mount, by name/key.
var defaultSecrets = map[string]string{
	"sidekick-db/url":    testDBURL,
	"anthropic/apiKey":   testAPIKey,
	"sidekick-slack/bot": testBotToken + "\n",
}

// parseConfig writes the secret files, fills SECRETS_DIR and the
// replacements into content, and parses it with regs.
func parseConfig(t *testing.T, content string, files map[string]string, regs config.Registries, replace ...string) *config.Config {
	t.Helper()

	dir := t.TempDir()
	for path, value := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(value), 0o600))
	}

	// The replacements come first, so they win over the defaults.
	r := strings.NewReplacer(slices.Concat(replace, []string{"SECRETS_DIR", dir, "ANTHROPIC_URL", "http://127.0.0.1:1"})...)

	cfg, err := config.Parse(t.Context(), []byte(r.Replace(content)), config.LoadOptions{Registries: regs})
	require.NoError(t, err)

	return cfg
}

// checks returns the Check names of results.
func checks(results []Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Check
	}

	return out
}

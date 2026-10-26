package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// runMainEnv makes the test binary run main instead of the tests, so a test
// can check the exit status of the real command.
const runMainEnv = "SIDEKICK_TEST_RUN_MAIN"

// validConfig is a valid config whose secrets live in the directory that
// replaces SECRETS_DIR.
const validConfig = `
secrets: { dir: "SECRETS_DIR" }
database:
  urlSecret: { name: sidekick-db, key: url }
model:
  connectors:
    claude: { provider: anthropic, model: claude-opus-5-5, apiKeySecret: { name: anthropic, key: apiKey } }
  roles:
    investigate: claude
routes:
  - name: default
`

var errFakeBuild = errors.New("dial failed")

// init runs main instead of the tests when runMainEnv is set, with the
// built-in plugins, as the released binary does. It is an init function
// rather than TestMain because the integration tests have their own
// TestMain.
func init() {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
}

type fakeModelConfig struct {
	model.Settings `yaml:",inline"`

	APIKeySecret *registry.SecretRef `yaml:"apiKeySecret"`
	FailBuild    bool                `yaml:"failBuild"`
}

func (*fakeModelConfig) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalling: true}
}

type fakeConnector struct{}

func (fakeConnector) Capabilities() model.Capabilities { return model.Capabilities{ToolCalling: true} }

func (fakeConnector) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errFakeBuild
}

// registerFakes registers a fake anthropic provider in r.
func registerFakes(r *registry.Registry[model.Connector]) {
	r.Register("anthropic", registry.Typed(func(_ context.Context, _ string, cfg fakeModelConfig, _ registry.Deps) (model.Connector, error) {
		if cfg.FailBuild {
			return nil, errFakeBuild
		}

		return fakeConnector{}, nil
	}))
}

func TestConfigValidateCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     string
		secrets    []string
		args       []string
		wantOut    string
		wantStderr []string
		wantErr    string
	}{
		{
			name:   "every problem on its own line",
			config: strings.Replace(validConfig, "investigate: claude", "investigate: claud", 1) + "servr: {}\n",
			args:   []string{"config", "validate", "--file", "FILE"},
			wantStderr: []string{
				`model.roles.investigate: connector "claud" is not defined (did you mean "claude"?)`,
				`servr: unknown key (did you mean "server"?)`,
			},
			wantErr: errInvalidConfig.Error(),
		},
		{
			name:       "missing file",
			args:       []string{"config", "validate", "-f", "MISSING"},
			wantStderr: []string{"load config: read config: open MISSING: no such file or directory"},
			wantErr:    errInvalidConfig.Error(),
		},
		{
			name:    "valid file, without reading secrets",
			config:  validConfig,
			args:    []string{"config", "validate", "-f", "FILE"},
			wantOut: "FILE is valid\n",
		},
		{
			name:    "check secrets with every file present",
			config:  validConfig,
			secrets: []string{"sidekick-db/url", "anthropic/apiKey"},
			args:    []string{"config", "validate", "-f", "FILE", "--check-secrets"},
			wantOut: "FILE is valid\n",
		},
		{
			name:    "check secrets with a missing file",
			config:  validConfig,
			secrets: []string{"sidekick-db/url"},
			args:    []string{"config", "validate", "-f", "FILE", "--check-secrets"},
			wantStderr: []string{
				"model.connectors.claude.apiKeySecret: cannot read secret anthropic/apiKey: the file does not exist",
			},
			wantErr: errInvalidConfig.Error(),
		},
		{
			name:       "check secrets builds every instance",
			config:     strings.Replace(validConfig, "model: claude-opus-5-5,", "model: claude-opus-5-5, failBuild: true,", 1),
			secrets:    []string{"sidekick-db/url", "anthropic/apiKey"},
			args:       []string{"config", "validate", "-f", "FILE", "--check-secrets"},
			wantStderr: []string{"model.connectors.claude: cannot build: dial failed"},
			wantErr:    errInvalidConfig.Error(),
		},
		{
			name:    "file flag is required",
			args:    []string{"config", "validate"},
			wantErr: `required flag(s) "file" not set`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			file := filepath.Join(dir, "sidekick.yaml")
			missing := filepath.Join(dir, "missing.yaml")
			secretsDir := writeSecrets(t, tt.secrets)

			if tt.config != "" {
				content := strings.ReplaceAll(tt.config, "SECRETS_DIR", secretsDir)
				require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
			}

			expand := func(s string) string {
				return strings.NewReplacer("FILE", file, "MISSING", missing).Replace(s)
			}

			args := make([]string, len(tt.args))
			for i, a := range tt.args {
				args[i] = expand(a)
			}

			regs := config.Registries{Models: registry.New[model.Connector]("model")}
			registerFakes(regs.Models)

			var stdout, stderr bytes.Buffer

			root := newRootCmd(buildinfo.Info{Version: "dev", Commit: "unknown"})
			root.RemoveCommand(findCommand(t, root, "config"))
			root.AddCommand(newConfigCmd(regs))
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(args)

			err := root.Execute()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
			} else {
				require.Empty(t, stderr.String())
				require.NoError(t, err)
			}

			require.Equal(t, expand(tt.wantOut), stdout.String())

			if tt.wantStderr != nil {
				require.Equal(t, expand(strings.Join(tt.wantStderr, "\n")+"\n"), stderr.String())
			}
		})
	}
}

func TestConfigValidateExitStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     string
		wantStatus int
	}{
		{name: "valid file exits 0", config: validConfig, wantStatus: 0},
		{name: "invalid file exits 1", config: validConfig + "servr: {}\n", wantStatus: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			file := filepath.Join(t.TempDir(), "sidekick.yaml")
			require.NoError(t, os.WriteFile(file, []byte(strings.ReplaceAll(tt.config, "SECRETS_DIR", t.TempDir())), 0o600))

			//nolint:gosec // Runs this test binary again, with fixed arguments.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "config", "validate", "-f", file)
			cmd.Env = append(os.Environ(), runMainEnv+"=1")

			err := cmd.Run()
			if tt.wantStatus == 0 {
				require.NoError(t, err)

				return
			}

			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			require.Equal(t, tt.wantStatus, exitErr.ExitCode())
		})
	}
}

// writeSecrets creates a secrets directory holding fake values for the
// given name/key paths and returns it.
func writeSecrets(t *testing.T, paths []string) string {
	t.Helper()

	dir := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte("fake-value\n"), 0o600))
	}

	return dir
}

// findCommand returns the subcommand of root called name.
func findCommand(t *testing.T, root *cobra.Command, name string) *cobra.Command {
	t.Helper()

	cmd, _, err := root.Find([]string{name})
	require.NoError(t, err)

	return cmd
}

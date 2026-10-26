package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// lazyClaude is a connector whose capabilities are known only once built.
const lazyClaude = `
model:
  connectors:
    claude: { provider: anthropic, model: claude-opus-5-5 }
    lazy: { provider: lazy, model: local-model }
  roles:
    investigate: claude
`

func TestBuild(t *testing.T) {
	t.Parallel()

	everything := minimalYAML(t) + `
sources:
  signoz: { type: mcp, url: https://signoz-mcp.internal/mcp }
  k8s: { type: kubernetes, namespaces: [prod] }
intake:
  webhooks:
    alertmanager: { adapter: alertmanager, auth: { bearer: { name: hooks, key: am } } }
outputs:
  slack-thread: { type: slack, channel: "#alerts" }
`

	tests := []struct {
		name        string
		yaml        string
		wantModels  []string
		wantSources []string
		wantIntakes []string
		wantOutputs []string
		wantErr     []string
	}{
		{
			name:        "every section is built",
			yaml:        everything,
			wantModels:  []string{"claude"},
			wantSources: []string{"k8s", "signoz"},
			wantIntakes: []string{"alertmanager"},
			wantOutputs: []string{"slack-thread"},
		},
		{
			name: "built connector without tool calling on the investigate role",
			yaml: "database: {urlSecret: {name: db, key: url}}\nroutes: [{name: default, roles: {investigate: lazy}}]\n" +
				replaceOnce(lazyClaude, "investigate: claude", "investigate: lazy"),
			wantErr: []string{
				`model.roles.investigate: connector "lazy" cannot call tools, which the investigate role needs`,
				`routes[0].roles.investigate: connector "lazy" cannot call tools, which the investigate role needs`,
			},
		},
		{
			name: "built connector with tool calling",
			yaml: "database: {urlSecret: {name: db, key: url}}\nroutes: [{name: default}]\n" +
				replaceOnce(replaceOnce(lazyClaude, "investigate: claude", "investigate: lazy"), "model: local-model", "model: local-model, toolCalling: true"),
			wantModels: []string{"claude", "lazy"},
		},
		{
			name: "constructor failure",
			yaml: "database: {urlSecret: {name: db, key: url}}\nroutes: [{name: default}]\n" +
				replaceOnce(lazyClaude, "model: local-model", "model: local-model, failBuild: true"),
			wantErr: []string{"model.connectors.lazy: cannot build: dial failed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := testOptions()
			cfg, err := Parse(t.Context(), []byte(tt.yaml), opts)
			require.NoError(t, err)

			in, err := Build(t.Context(), cfg, opts.Registries, registry.Deps{})
			if tt.wantErr != nil {
				require.Nil(t, in)

				var errs Errors
				require.ErrorAs(t, err, &errs)
				require.Equal(t, strings.Join(tt.wantErr, "\n"), errs.Error())

				return
			}

			require.NoError(t, err)
			require.ElementsMatch(t, tt.wantModels, sortedKeys(in.Models))
			require.ElementsMatch(t, tt.wantSources, sortedKeys(in.Sources))
			require.ElementsMatch(t, tt.wantIntakes, sortedKeys(in.Intakes))
			require.ElementsMatch(t, tt.wantOutputs, sortedKeys(in.Outputs))
			require.NoError(t, in.Close())
		})
	}
}

func TestBuildDeps(t *testing.T) {
	t.Parallel()

	given := secrets.NewDir(t.TempDir())

	tests := []struct {
		name string
		deps registry.Deps
		want registry.Secrets
	}{
		{name: "secrets.dir by default", deps: registry.Deps{}, want: secrets.NewDir("/etc/sidekick-secrets")},
		{name: "given resolver", deps: registry.Deps{Secrets: given}, want: given},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			regs := testRegistries()
			regs.Models = registry.New[model.Connector]("model")

			var got registry.Secrets

			regs.Models.Register("anthropic", registry.Plugin[model.Connector]{
				Build: func(_ context.Context, _ string, _ registry.Decoder, deps registry.Deps) (model.Connector, error) {
					got = deps.Secrets

					return fakeConnector{caps: model.Capabilities{ToolCalling: true}}, nil
				},
			})

			cfg, err := Parse(t.Context(), []byte(minimalYAML(t)+"secrets: {dir: /etc/sidekick-secrets}\n"), LoadOptions{Registries: regs})
			require.NoError(t, err)

			_, err = Build(t.Context(), cfg, regs, tt.deps)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBuildClosesSourcesOnFailure(t *testing.T) {
	t.Parallel()

	errClose := errors.New("close failed")

	tests := []struct {
		name     string
		closeErr error
		wantErr  string
	}{
		{name: "sources are closed", wantErr: "model.connectors.lazy: cannot build: dial failed"},
		{
			name:     "close failures are reported too",
			closeErr: errClose,
			wantErr:  "model.connectors.lazy: cannot build: dial failed\n" + `clean up after failed build: close source "signoz": close failed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := source.NewMockSource(gomock.NewController(t))
			src.EXPECT().Close().Return(tt.closeErr)

			regs := testRegistries()
			regs.Sources = registry.New[source.Source]("source")
			regs.Sources.Register("mcp", registry.Plugin[source.Source]{
				Build: func(context.Context, string, registry.Decoder, registry.Deps) (source.Source, error) {
					return src, nil
				},
			})

			yaml := "database: {urlSecret: {name: db, key: url}}\nroutes: [{name: default}]\nsources: {signoz: {type: mcp}}\n" +
				replaceOnce(lazyClaude, "model: local-model", "model: local-model, failBuild: true")

			cfg, err := Parse(t.Context(), []byte(yaml), LoadOptions{Registries: regs})
			require.NoError(t, err)

			in, err := Build(t.Context(), cfg, regs, registry.Deps{})
			require.Nil(t, in)
			require.EqualError(t, err, tt.wantErr)

			if tt.closeErr != nil {
				require.ErrorIs(t, err, tt.closeErr)
			}
		})
	}
}

// replaceOnce replaces the first old in s, and fails the test run when s
// does not hold old.
func replaceOnce(s, old, replacement string) string {
	if !strings.Contains(s, old) {
		panic("replaceOnce: " + old + " not found")
	}

	return strings.Replace(s, old, replacement, 1)
}

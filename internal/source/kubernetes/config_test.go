package kubernetes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	kubeconfig := &registry.SecretRef{Name: "other-cluster", Key: "kubeconfig"}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the service account and every namespace", cfg: Config{}},
		{name: "namespaces and logs", cfg: Config{Namespaces: []string{"prod", "staging"}, Logs: true}},
		{name: "a kubeconfig with a context", cfg: Config{KubeconfigSecret: kubeconfig, Context: "prod-eu"}},
		{name: "a console URL", cfg: Config{ConsoleURL: testConsoleURL}},
		{
			name: "a console URL with a condition",
			cfg:  Config{ConsoleURL: "https://argocd.example.com/{{.Resource}}{{if .Namespace}}/{{.Namespace}}{{end}}/{{.Name}}"},
		},
		{
			name: "bad and repeated namespaces",
			cfg:  Config{Namespaces: []string{"prod", "Prod", "prod", ""}},
			want: []string{`namespaces[1]: "Prod" is not a valid namespace name`, `namespaces[2]: "prod" is listed twice`, `namespaces[3]: "" is not a valid namespace name`},
		},
		{name: "a context without a kubeconfig", cfg: Config{Context: "prod-eu"}, want: []string{"context: only with kubeconfigSecret"}},
		{name: "a template that does not parse", cfg: Config{ConsoleURL: "https://x/{{.Name"}, want: []string{"consoleURL: parse the template:"}},
		{name: "a template with an unknown field", cfg: Config{ConsoleURL: "https://x/{{.Cluster}}"}, want: []string{"consoleURL: render the template:"}},
		{
			name: "a template that is not an absolute URL",
			cfg:  Config{ConsoleURL: "/{{.Resource}}/{{.Name}}"},
			want: []string{`consoleURL: "/{{.Resource}}/{{.Name}}" must render an absolute http or https URL, not "/deployments/example"`},
		},
		{name: "a template with another scheme", cfg: Config{ConsoleURL: "ftp://x/{{.Name}}"}, want: []string{"must render an absolute http or https URL"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			lines := strings.Split(err.Error(), "\n")
			require.Len(t, lines, len(tt.want))

			for i, want := range tt.want {
				require.Contains(t, lines[i], want)
			}
		})
	}
}

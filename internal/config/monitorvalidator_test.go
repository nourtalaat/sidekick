package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestMonitorValidatorSection(t *testing.T) {
	t.Parallel()

	minimal := minimalYAML(t)

	tests := []struct {
		name     string
		yaml     string
		want     MonitorValidator
		problems []string
	}{
		{name: "the defaults", yaml: minimal, want: MonitorValidator{Mode: ValidatorModeSidecar, URL: DefaultValidatorURL}},
		{
			name: "a url and the chart's setting", yaml: minimal + "validator: {url: 'http://validator.ops.svc:8200', allowDownloads: true}\n",
			want: MonitorValidator{Mode: ValidatorModeSidecar, URL: "http://validator.ops.svc:8200", AllowDownloads: true},
		},
		{
			name: "job mode is not available", yaml: minimal + "validator: {mode: job}\n",
			problems: []string{"validator.mode: job mode is not available yet; use sidecar"},
		},
		{
			name: "an unknown mode", yaml: minimal + "validator: {mode: sidecr}\n",
			problems: []string{`validator.mode: expected sidecar or job, got "sidecr" (did you mean "sidecar"?)`},
		},
		{
			name: "a url that is not http", yaml: minimal + "validator: {url: 'validator:8200'}\n",
			problems: []string{`validator.url: expected an absolute http or https URL such as http://127.0.0.1:8200, got "validator:8200"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Parse(t.Context(), []byte(tt.yaml), testOptions())
			if len(tt.problems) > 0 {
				requireProblems(t, err, tt.problems)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, cfg.Validator)

			_, err = Build(t.Context(), cfg, testRegistries(), registry.Deps{})
			require.NoError(t, err)
		})
	}
}

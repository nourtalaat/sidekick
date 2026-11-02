package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/validateclient"
	"github.com/nourtalaat/sidekick/internal/validateclient/validatejob"
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
			name: "job mode with what the chart sets",
			yaml: minimal + `validator:
  mode: job
  allowDownloads: true
  job:
    image: ghcr.io/nourtalaat/sidekick-validator:0.1.0
    imagePullPolicy: IfNotPresent
    imagePullSecrets: [registry-login]
    serviceAccountName: sidekick-validator
    resources:
      requests: {cpu: 100m, memory: 256Mi}
      limits: {memory: 1Gi}
    workDirSizeLimit: 2Gi
    podLabels:
      app.kubernetes.io/name: sidekick
      app.kubernetes.io/component: validator
`,
			want: MonitorValidator{
				Mode: ValidatorModeJob, URL: DefaultValidatorURL, AllowDownloads: true,
				Job: ValidatorJob{
					Image:              "ghcr.io/nourtalaat/sidekick-validator:0.1.0",
					ImagePullPolicy:    "IfNotPresent",
					ImagePullSecrets:   []string{"registry-login"},
					ServiceAccountName: "sidekick-validator",
					Resources:          ValidatorResources{Requests: map[string]string{"cpu": "100m", "memory": "256Mi"}, Limits: map[string]string{"memory": "1Gi"}},
					WorkDirSizeLimit:   "2Gi",
					PodLabels:          map[string]string{"app.kubernetes.io/name": "sidekick", "app.kubernetes.io/component": "validator"},
				},
			},
		},
		{
			name: "job mode with only an image", yaml: minimal + "validator: {mode: job, job: {image: 'validator:1'}}\n",
			want: MonitorValidator{Mode: ValidatorModeJob, URL: DefaultValidatorURL, Job: ValidatorJob{Image: "validator:1"}},
		},
		{
			name: "job mode without an image", yaml: minimal + "validator: {mode: job}\n",
			problems: []string{"validator.job.image: required in job mode; the chart sets it when validator.mode is job"},
		},
		{
			name: "a job section that is wrong",
			yaml: minimal + `validator:
  mode: job
  job:
    image: validator:1
    imagePullPolicy: Alwayz
    resources: {requests: {cpu: lots}, limits: {memory: 1 gig}}
    workDirSizeLimit: big
`,
			problems: []string{
				`validator.job.imagePullPolicy: expected Always, IfNotPresent or Never, got "Alwayz" (did you mean "Always"?)`,
				`validator.job.resources.limits: memory "1 gig": quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'`,
				`validator.job.resources.requests: cpu "lots": quantities must match the regular expression '^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$'`,
				`validator.job.workDirSizeLimit: expected a quantity such as 2Gi, got "big"`,
			},
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

func TestMonitorValidatorClient(t *testing.T) {
	t.Parallel()

	t.Run("the sidecar client", func(t *testing.T) {
		t.Parallel()

		m := MonitorValidator{Mode: ValidatorModeSidecar, URL: "http://127.0.0.1:8200"}

		got, err := m.Validator(nil, nil)
		require.NoError(t, err)
		require.IsType(t, &validateclient.Client{}, got)
	})

	t.Run("the job-mode client", func(t *testing.T) {
		t.Parallel()

		m := MonitorValidator{Mode: ValidatorModeJob, Job: ValidatorJob{Image: "validator:1", WorkDirSizeLimit: "2Gi", Resources: ValidatorResources{Limits: map[string]string{"memory": "1Gi"}}}}

		got, err := m.Validator(nil, nil)
		require.NoError(t, err)
		require.IsType(t, &validatejob.Client{}, got)
	})

	t.Run("a job section with a bad quantity", func(t *testing.T) {
		t.Parallel()

		m := MonitorValidator{Mode: ValidatorModeJob, Job: ValidatorJob{Image: "validator:1", WorkDirSizeLimit: "big"}}

		_, err := m.Validator(nil, nil)
		require.ErrorContains(t, err, "validator.job: workDirSizeLimit")
	})

	t.Run("pod labels that Kubernetes would refuse", func(t *testing.T) {
		t.Parallel()

		yaml := minimalYAML(t) + `validator: {mode: job, job: {image: 'validator:1', podLabels: {"not a key": x, ok: "bad value!"}}}` + "\n"

		_, err := Parse(t.Context(), []byte(yaml), testOptions())
		require.ErrorContains(t, err, `validator.job.podLabels: label "not a key"`)
		require.ErrorContains(t, err, `validator.job.podLabels: label "ok": value "bad value!"`)
	})

	t.Run("a validator in the deps is kept", func(t *testing.T) {
		t.Parallel()

		cfg, err := Parse(t.Context(), []byte(minimalYAML(t)), testOptions())
		require.NoError(t, err)

		kept := validateclient.New("http://kept.example.com", nil)

		_, err = Build(t.Context(), cfg, testRegistries(), registry.Deps{Validator: kept})
		require.NoError(t, err)
	})
}

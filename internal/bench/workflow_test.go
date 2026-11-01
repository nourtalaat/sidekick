package bench

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// benchWorkflow is the benchmark workflow, which runs with a repository
// secret in a public repository.
var benchWorkflow = filepath.Join(repoRoot, ".github", "workflows", "bench.yml")

// pinnedAction is an action reference that names a full commit SHA.
var pinnedAction = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

// workflowFile is the part of a workflow the safety checks read.
type workflowFile struct {
	On          map[string]any         `yaml:"on"`
	Permissions map[string]string      `yaml:"permissions"`
	Concurrency workflowConcurrency    `yaml:"concurrency"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

// workflowConcurrency is the concurrency setting of a workflow.
type workflowConcurrency struct {
	CancelInProgress bool `yaml:"cancel-in-progress"`
}

// workflowJob is one job of a workflow.
type workflowJob struct {
	If          string            `yaml:"if"`
	Needs       any               `yaml:"needs"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []workflowStep    `yaml:"steps"`
}

// workflowStep is one step of a job.
type workflowStep struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	With map[string]any `yaml:"with"`
}

func TestBenchWorkflowIsSafeForAPublicRepository(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(benchWorkflow) //nolint:gosec // A fixed path in the repository.
	require.NoError(t, err)

	var wf workflowFile
	require.NoError(t, yaml.Unmarshal(raw, &wf))

	t.Run("it runs only on the nightly schedule and by hand", func(t *testing.T) {
		t.Parallel()

		triggers := make([]string, 0, len(wf.On))
		for trigger := range wf.On {
			triggers = append(triggers, trigger)
		}

		require.ElementsMatch(t, []string{"schedule", "workflow_dispatch"}, triggers)

		for _, trigger := range []string{"push", "pull_request", "pull_request_target"} {
			require.NotContains(t, wf.On, trigger, "an event a stranger can cause must not start a run with the secret")
		}
	})

	t.Run("a run in progress is never cancelled", func(t *testing.T) {
		t.Parallel()

		require.False(t, wf.Concurrency.CancelInProgress)
	})

	t.Run("the default permissions only read", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, map[string]string{"contents": "read"}, wf.Permissions)
	})

	t.Run("every action is pinned by commit SHA", func(t *testing.T) {
		t.Parallel()

		for name, job := range wf.Jobs {
			for _, step := range job.Steps {
				if step.Uses != "" {
					require.Regexp(t, pinnedAction, step.Uses, "job %s", name)
				}

				if strings.HasPrefix(step.Uses, "actions/checkout@") {
					keep, ok := step.With["persist-credentials"].(bool)
					require.True(t, ok, "job %s sets persist-credentials", name)
					require.False(t, keep, "job %s keeps no token in the checkout", name)
				}
			}
		}
	})

	t.Run("the only job runs the benchmark and can only read the repository", func(t *testing.T) {
		t.Parallel()

		require.Len(t, wf.Jobs, 1)
		require.Contains(t, wf.Jobs, "bench")
		require.Empty(t, wf.Jobs["bench"].If, "the job has no condition that could let an event through")
		require.Equal(t, map[string]string{"contents": "read"}, wf.Jobs["bench"].Permissions)
	})

	t.Run("it skips with a notice when the key is not set", func(t *testing.T) {
		t.Parallel()

		require.Contains(t, string(raw), "-z \"$BENCH_ANTHROPIC_API_KEY\"")
		require.Contains(t, string(raw), "::notice title=Benchmark skipped::")
	})

	t.Run("the only secret is the benchmark key", func(t *testing.T) {
		t.Parallel()

		for _, ref := range regexp.MustCompile(`secrets\.[A-Za-z0-9_]+`).FindAllString(string(raw), -1) {
			require.Equal(t, "secrets.BENCH_ANTHROPIC_API_KEY", ref)
		}
	})
}

func TestBenchSettingsOfTheWorkflow(t *testing.T) {
	t.Parallel()

	file := filepath.Join(repoRoot, "bench", "bench.yaml")

	settings, err := LoadSettings(file)
	require.NoError(t, err)
	require.InDelta(t, 0.8, settings.Thresholds.VerdictAccuracy, 0)
	require.Empty(t, settings.Thresholds.Baseline)

	t.Run("with the baseline lines the workflow adds", func(t *testing.T) {
		t.Parallel()

		raw, err := os.ReadFile(file) //nolint:gosec // A fixed path in the repository.
		require.NoError(t, err)

		withBaseline := filepath.Join(t.TempDir(), "bench.yaml")
		require.NoError(t, os.WriteFile(withBaseline, append(raw, []byte("  baseline: bench/baseline.json\n  maxDrop: 0.02\n")...), 0o600)) //nolint:gosec // A file in the test's own temporary directory.

		got, err := LoadSettings(withBaseline)
		require.NoError(t, err)
		require.Equal(t, "bench/baseline.json", got.Thresholds.Baseline)
	})
}

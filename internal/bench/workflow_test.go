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
	Jobs        map[string]workflowJob `yaml:"jobs"`
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

	t.Run("it runs on the nightly schedule, release tags, labeled pull requests and by hand", func(t *testing.T) {
		t.Parallel()

		for _, trigger := range []string{"schedule", "push", "pull_request", "workflow_dispatch"} {
			require.Contains(t, wf.On, trigger)
		}

		require.NotContains(t, wf.On, "pull_request_target", "it would give a fork's code the secret")
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

	t.Run("a pull request runs only when labeled bench and from this repository", func(t *testing.T) {
		t.Parallel()

		bench := wf.Jobs["bench"]
		require.Contains(t, bench.If, "github.event_name != 'pull_request'")
		require.Contains(t, bench.If, "github.event.pull_request.head.repo.full_name == github.repository")
		require.Contains(t, bench.If, "contains(github.event.pull_request.labels.*.name, 'bench')")
	})

	t.Run("only the job that runs the benchmark sees the key, and it cannot write", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, map[string]string{"contents": "read"}, wf.Jobs["bench"].Permissions)
		require.Equal(t, map[string]string{"pull-requests": "write"}, wf.Jobs["comment"].Permissions)

		_, comment, found := strings.Cut(string(raw), "\n  comment:\n")
		require.True(t, found)
		require.NotContains(t, comment, "secrets.", "the job that writes the comment has no secret")
		require.NotContains(t, comment, "actions/checkout", "and no checkout of the pull request's code")
		require.Contains(t, wf.Jobs["comment"].If, "github.event_name == 'pull_request'")
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

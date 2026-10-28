//go:build integration

package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// repoRoot is the directory `sidekick bench run` runs in, so the sample
// scenario's snapshot paths resolve.
const repoRoot = "../.."

// overloaded is the body of an Anthropic overloaded error.
const overloaded = `{"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}`

// statusOverloaded is the status of an Anthropic overloaded error.
const statusOverloaded = 529

// TestBenchRun runs `sidekick bench run` on the sample scenario as its own
// process, as a user would: once with a temporary Postgres that it starts
// in Docker and stops, and with --db in a schema of its own that it drops.
func TestBenchRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		db   bool
		// outputs adds --bench-config with a verdict accuracy threshold,
		// --out, --junit and --markdown.
		outputs bool
		// oldSchema leaves the database's newest migration out.
		oldSchema bool
		// wantStored is how many bench_runs rows the run leaves.
		wantStored int
		// anthropic answers the model calls.
		anthropic  func(t *testing.T) *fakehttp.Server
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "a temporary Postgres",
			anthropic:  inconclusiveAnthropic,
			wantStdout: []string{"SCENARIO", "checkout-pool-size", "1/1", "inconclusive", "true_positive", "overall score"},
			wantStderr: []string{"starting a temporary Postgres container", "stopped the temporary Postgres container"},
		},
		{
			name:       "--db, in a schema of its own",
			db:         true,
			anthropic:  inconclusiveAnthropic,
			wantStdout: []string{"checkout-pool-size", "1/1", "inconclusive", "root cause match"},
			wantStderr: []string{"created the benchmark's schema", "dropped the benchmark's schema", "stored the results in bench_runs and bench_results"},
			wantStored: 1,
		},
		{
			name:       "--db on a database that needs sidekick migrate",
			db:         true,
			oldSchema:  true,
			anthropic:  func(t *testing.T) *fakehttp.Server { t.Helper(); return fakeAnthropic(t) },
			wantCode:   exitBadInput,
			wantStderr: []string{"run sidekick migrate first"},
		},
		{
			name:       "the output files and a failed threshold",
			db:         true,
			outputs:    true,
			anthropic:  inconclusiveAnthropic,
			wantCode:   exitThresholdFailed,
			wantStdout: []string{"THRESHOLD", "verdictAccuracy", "FAILED"},
			wantStderr: []string{"1 of 1 thresholds failed: verdictAccuracy"},
			wantStored: 1,
		},
		{
			name: "the model provider is unreachable",
			db:   true,
			anthropic: func(t *testing.T) *fakehttp.Server {
				t.Helper()

				api := fakehttp.New(t)
				api.Handle(http.MethodPost, messagesPath, fakehttp.Response{Status: statusOverloaded, Body: []byte(overloaded), Headers: map[string]string{"Content-Type": "application/json"}})
				api.ExpectAllCalled()

				return api
			},
			wantCode:   exitInfrastructure,
			wantStdout: []string{"SCENARIO"},
			wantStderr: []string{"the model provider is unreachable after retries", "dropped the benchmark's schema"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dbURL := storetest.NewURL(t)
			configPath := writeConfig(t, serveConfig(tt.anthropic(t).URL()), dbURL)

			if tt.oldSchema {
				execSQL(t, dbURL, "DELETE FROM goose_db_version WHERE version_id = (SELECT max(version_id) FROM goose_db_version)")
			}

			sample, err := filepath.Abs(filepath.Join(repoRoot, "bench", "scenarios", "sample"))
			require.NoError(t, err)

			args := []string{"bench", "run", "--scenarios", sample, "--config", configPath, "--repeat", "1"}
			if tt.db {
				args = append(args, "--db")
			}

			out := t.TempDir()
			if tt.outputs {
				settings := filepath.Join(out, "bench.yaml")
				require.NoError(t, os.WriteFile(settings, []byte("thresholds: { verdictAccuracy: 0.5 }\n"), 0o600))

				args = append(args, "--bench-config", settings, "--label", "integration",
					"--out", filepath.Join(out, "report.json"), "--junit", filepath.Join(out, "junit.xml"), "--markdown", filepath.Join(out, "summary.md"))
			}

			code, stdout, stderr := runBench(t, args)
			require.Equal(t, tt.wantCode, code, "stderr:\n%s", stderr)

			for _, want := range tt.wantStdout {
				require.Contains(t, stdout, want)
			}

			for _, want := range tt.wantStderr {
				require.Contains(t, stderr, want)
			}

			require.Equal(t, 0, benchSchemas(t, dbURL), "no benchmark schema is left behind")

			if !tt.oldSchema {
				require.Equal(t, tt.wantStored, countRows(t, dbURL, "bench_runs"))
				require.Equal(t, tt.wantStored, countRows(t, dbURL, "bench_results"), "one result per run")
			}

			if tt.outputs {
				rep, err := bench.ReadReport(filepath.Join(out, "report.json"))
				require.NoError(t, err)
				require.Equal(t, "integration", rep.Label)
				require.Len(t, rep.Scenarios, 1)
				require.Len(t, rep.Thresholds, 1)
				require.False(t, rep.Thresholds[0].Passed)
				require.NotNil(t, rep.Scenarios[0].Runs[0].RootCause, "the judge graded the run")

				junit, err := os.ReadFile(filepath.Join(out, "junit.xml"))
				require.NoError(t, err)
				require.Contains(t, string(junit), `<testsuite name="checkout-pool-size"`)

				summary, err := os.ReadFile(filepath.Join(out, "summary.md"))
				require.NoError(t, err)
				require.Contains(t, string(summary), "**1 of 1 thresholds failed.**")
			}
		})
	}
}

// TestBenchJudgeCheck runs `sidekick bench judge-check` on a shipped case
// as its own process, with the judge model answered by a fake Anthropic
// API that grades the fix item as not met where the hand grade says met.
func TestBenchJudgeCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
	}{
		{
			name:       "the agreement with the hand grades",
			wantStdout: []string{"pool-full", "fix", "agreement: 0.67 (2 of 3 items)"},
		},
		{
			name:       "an agreement below --min-agreement",
			args:       []string{"--min-agreement", "0.9"},
			wantCode:   exitThresholdFailed,
			wantStdout: []string{"agreement: 0.67"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			configPath := writeConfig(t, serveConfig(fakeAnthropic(t, "fixture/anthropic-grades.sse").URL()), "")

			args := append([]string{"bench", "judge-check", "--cases", "bench/judge-check/pool-full.json", "--config", configPath}, tt.args...)

			code, stdout, stderr := runBench(t, args)
			require.Equal(t, tt.wantCode, code, "stderr:\n%s", stderr)

			for _, want := range tt.wantStdout {
				require.Contains(t, stdout, want)
			}
		})
	}
}

// inconclusiveAnthropic answers the investigation's model call with an
// inconclusive verdict, and the judge's with grades.
func inconclusiveAnthropic(t *testing.T) *fakehttp.Server {
	t.Helper()

	return fakeAnthropic(t, "fixture/anthropic-inconclusive.sse", "fixture/anthropic-grades.sse")
}

// runBench runs this test binary as `sidekick` with args in the repository
// root, and returns its exit code and output.
func runBench(t *testing.T, args []string) (code int, stdout, stderr string) {
	t.Helper()

	self, err := filepath.Abs(os.Args[0])
	require.NoError(t, err)

	root, err := filepath.Abs(repoRoot)
	require.NoError(t, err)

	var out, errOut bytes.Buffer

	//nolint:gosec // Runs this test binary again, with fixed arguments.
	cmd := exec.CommandContext(t.Context(), self, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err = cmd.Run()

	var exitErr *exec.ExitError

	switch {
	case err == nil:
		code = 0
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		require.NoError(t, err)
	}

	return code, out.String(), errOut.String()
}

// execSQL runs sql on the database at dbURL.
func execSQL(t *testing.T, dbURL, sql string) {
	t.Helper()

	pool, err := store.Open(t.Context(), dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	_, err = pool.Exec(t.Context(), sql)
	require.NoError(t, err)
}

// countRows counts the rows of table in the default schema of the
// database at dbURL.
func countRows(t *testing.T, dbURL, table string) int {
	t.Helper()

	pool, err := store.Open(t.Context(), dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n))

	return n
}

// benchSchemas counts the benchmark schemas in the database at dbURL.
func benchSchemas(t *testing.T, dbURL string) int {
	t.Helper()

	pool, err := store.Open(t.Context(), dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	var n int
	require.NoError(t, pool.QueryRow(t.Context(),
		"SELECT count(*) FROM pg_namespace WHERE starts_with(nspname, $1)", bench.SchemaPrefix).Scan(&n))

	return n
}

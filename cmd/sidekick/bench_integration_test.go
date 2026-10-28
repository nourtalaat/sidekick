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
		// anthropic answers the model calls.
		anthropic  func(t *testing.T) *fakehttp.Server
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "a temporary Postgres",
			anthropic:  inconclusiveAnthropic,
			wantStdout: []string{"SCENARIO", "checkout-pool-size", "1/1", "inconclusive", "true_positive"},
			wantStderr: []string{"starting a temporary Postgres container", "stopped the temporary Postgres container"},
		},
		{
			name:       "--db, in a schema of its own",
			db:         true,
			anthropic:  inconclusiveAnthropic,
			wantStdout: []string{"checkout-pool-size", "1/1", "inconclusive"},
			wantStderr: []string{"created the benchmark's schema", "dropped the benchmark's schema"},
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

			sample, err := filepath.Abs(filepath.Join(repoRoot, "bench", "scenarios", "sample"))
			require.NoError(t, err)

			args := []string{"bench", "run", "--scenarios", sample, "--config", configPath, "--repeat", "1"}
			if tt.db {
				args = append(args, "--db")
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
		})
	}
}

// inconclusiveAnthropic answers one model call with an inconclusive
// verdict.
func inconclusiveAnthropic(t *testing.T) *fakehttp.Server {
	t.Helper()

	return fakeAnthropic(t, "fixture/anthropic-inconclusive.sse")
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

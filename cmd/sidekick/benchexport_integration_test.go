//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The investigations in fixture/scenario_export.sql.
const (
	scnInvestigation = "0198f2a4-0000-7000-8000-0000000000c1"
	scnUnconfirmed   = "0198f2a4-0000-7000-8000-0000000000c3"
	scnMissing       = "0198f2a4-0000-7000-8000-0000000000ff"
)

// exportSetup is a database with the fixture's investigation and the path of
// a config file over it.
type exportSetup struct {
	dbURL  string
	config string
	// anthropic is the fake provider, and nil without a summarize role.
	anthropic *fakehttp.Server
}

// newExportSetup seeds a database. With rubric set, the config has a
// summarize role and the fake provider answers one rubric call.
func newExportSetup(t *testing.T, rubric bool) exportSetup {
	t.Helper()

	dbURL := storetest.NewURL(t)

	pool, err := store.Open(t.Context(), dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	storetest.LoadFixture(t, pool, "fixture/scenario_export.sql")

	s := exportSetup{dbURL: dbURL}

	content := webhookConfig(fakeAnthropic(t).URL())

	if rubric {
		s.anthropic = fakeAnthropic(t, "fixture/anthropic-rubric.sse")
		content = strings.Replace(webhookConfig(s.anthropic.URL()), "    investigate: claude\n", "    investigate: claude\n    summarize: claude\n", 1)
	}

	s.config = writeConfig(t, content, dbURL, "hooks/alertmanager")

	return s
}

// queryOne runs a query that returns one text value.
func queryOne(t *testing.T, dbURL, sql string, args ...any) string {
	t.Helper()

	// The context outlives the test, since a cleanup function may ask.
	ctx := context.WithoutCancel(t.Context())

	pool, err := store.Open(ctx, dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	var out string

	require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&out))

	return out
}

// reviewScenario marks a scenario as reviewed, as the web UI does.
func reviewScenario(t *testing.T, pool *pgxpool.Pool, id string) {
	t.Helper()

	_, err := pool.Exec(t.Context(), `INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
	                                  VALUES ('0198f2a4-0000-7000-8000-0000000000e1', 'https://idp.example.com', 'rev', 'Rev', 'admin', now(), now())`)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), `UPDATE scenarios SET status = 'reviewed', reviewed_by = '0198f2a4-0000-7000-8000-0000000000e1', reviewed_at = now() WHERE id = $1`, id)
	require.NoError(t, err)
}

// TestBenchExport runs `sidekick bench export` as its own process: it builds
// the draft of a confirmed investigation, refuses to write the draft out,
// and writes it once it is reviewed.
func TestBenchExport(t *testing.T) {
	t.Parallel()

	s := newExportSetup(t, true)

	// Exporting builds a draft and prints its ID.
	code, stdout, stderr := runBench(t, []string{"bench", "export", "--investigation", scnInvestigation, "--config", s.config})
	require.Equal(t, 0, code, "stderr:\n%s", stderr)

	id := strings.TrimSpace(stdout)
	require.NotEmpty(t, id)
	require.Contains(t, stderr, "note:")
	require.NotContains(t, stderr, "model.roles.summarize is not set")

	require.Equal(t, "draft", queryOne(t, s.dbURL, `SELECT status FROM scenarios WHERE id = $1`, id))
	require.Equal(t, "system cli", queryOne(t, s.dbURL, `SELECT actor_kind || ' ' || actor_id FROM audit_log WHERE action = 'scenario.drafted' AND target_id = $1`, id))
	require.Equal(t, "1", queryOne(t, s.dbURL, `SELECT count(*)::text FROM spend_daily`), "the rubric call is in the day's spend")

	// The model saw placeholders, never the host name.
	reqs := s.anthropic.Requests()
	require.Len(t, reqs, 1)
	require.Contains(t, string(reqs[0].Body), "Pool exhausted on host-1 after host-2 refused connections.")
	require.NotContains(t, string(reqs[0].Body), "acme.com")

	// The rubric the model drafted is in the file, cleaned.
	body := queryOne(t, s.dbURL, `SELECT body FROM scenarios WHERE id = $1`, id)
	sc, err := bench.ParseScenario([]byte(body))
	require.NoError(t, err)
	require.Len(t, sc.Expected.RootCause.Rubric, 2)
	require.Contains(t, sc.Expected.RootCause.Rubric[0].Text, "host-1")
	require.NotContains(t, body, "acme.com")

	// A second export finds the same scenario.
	code, stdout, stderr = runBench(t, []string{"bench", "export", "--investigation", scnInvestigation, "--config", s.config})
	require.Equal(t, 0, code, "stderr:\n%s", stderr)
	require.Equal(t, id, strings.TrimSpace(stdout))
	require.Contains(t, stderr, "has a scenario already")

	// A draft is not written out.
	out := filepath.Join(t.TempDir(), "scenario.yaml")

	code, _, stderr = runBench(t, []string{"bench", "export", "--scenario", id, "--out", out, "--config", s.config})
	require.Equal(t, exitFailure, code)
	require.Contains(t, stderr, "not reviewed")
	require.NoFileExists(t, out)

	pool, err := store.Open(t.Context(), s.dbURL, 1)
	require.NoError(t, err)

	defer pool.Close()

	reviewScenario(t, pool, id)

	// A reviewed scenario is written out, once.
	code, stdout, stderr = runBench(t, []string{"bench", "export", "--scenario", id, "--out", out, "--config", s.config})
	require.Equal(t, 0, code, "stderr:\n%s", stderr)
	require.Equal(t, out, strings.TrimSpace(stdout))

	written, err := os.ReadFile(out) //nolint:gosec // A file in the test's own temporary directory.
	require.NoError(t, err)
	require.Equal(t, body, string(written))
	require.Equal(t, "1", queryOne(t, s.dbURL, `SELECT count(*)::text FROM audit_log WHERE action = 'scenario.downloaded' AND target_id = $1`, id))

	code, _, stderr = runBench(t, []string{"bench", "export", "--scenario", id, "--out", out, "--config", s.config})
	require.Equal(t, exitBadInput, code)
	require.Contains(t, stderr, "use --force")

	code, _, stderr = runBench(t, []string{"bench", "export", "--scenario", id, "--out", out, "--force", "--config", s.config})
	require.Equal(t, 0, code, "stderr:\n%s", stderr)
}

func TestBenchExportRefusals(t *testing.T) {
	t.Parallel()

	s := newExportSetup(t, false)

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStderr string
	}{
		{name: "an investigation that is not confirmed", args: []string{"--investigation", scnUnconfirmed}, wantCode: exitFailure, wantStderr: "no confirmed verdict"},
		{name: "an investigation that does not exist", args: []string{"--investigation", scnMissing}, wantCode: exitBadInput, wantStderr: "investigation not found"},
		{name: "an ID that is not an ID", args: []string{"--investigation", "nope"}, wantCode: exitBadInput, wantStderr: "must be an investigation ID"},
		{name: "a scenario that does not exist", args: []string{"--scenario", "no-such-scenario", "--out", filepath.Join(t.TempDir(), "x.yaml")}, wantCode: exitBadInput, wantStderr: "scenario not found"},
		{name: "--out without --scenario", args: []string{"--investigation", scnInvestigation, "--out", filepath.Join(t.TempDir(), "x.yaml")}, wantCode: exitBadInput, wantStderr: "--out goes with --scenario"},
		{name: "--scenario without --out", args: []string{"--scenario", "x"}, wantCode: exitBadInput, wantStderr: "--out goes with --scenario"},
	}

	t.Cleanup(func() {
		require.Equal(t, "0", queryOne(t, s.dbURL, `SELECT count(*)::text FROM scenarios`))
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, _, stderr := runBench(t, append([]string{"bench", "export", "--config", s.config}, tt.args...))
			require.Equal(t, tt.wantCode, code, "stderr:\n%s", stderr)
			require.Contains(t, stderr, tt.wantStderr)
		})
	}
}

func TestBenchExportWithoutSummarizeRole(t *testing.T) {
	t.Parallel()

	s := newExportSetup(t, false)

	code, stdout, stderr := runBench(t, []string{"bench", "export", "--investigation", scnInvestigation, "--config", s.config})
	require.Equal(t, 0, code, "stderr:\n%s", stderr)
	require.NotEmpty(t, strings.TrimSpace(stdout))
	require.Contains(t, stderr, "model.roles.summarize is not set")

	body := queryOne(t, s.dbURL, `SELECT body FROM scenarios`)
	sc, err := bench.ParseScenario([]byte(body))
	require.NoError(t, err)
	require.Empty(t, sc.Expected.RootCause.Rubric)
}

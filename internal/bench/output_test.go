package bench

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
)

// reportSchemaFile is the published schema of report.json.
const reportSchemaFile = "../../docs/bench-report.schema.json"

// testInfo is the run info of the output tests.
var testInfo = RunInfo{
	SidekickVersion: "v0.1.0", GitSHA: "abc1234", ConfigHash: "c0ffee", ScenarioSet: "bench/scenarios/sample", TriggeredBy: "cli",
	Connectors: map[model.Role]ConnectorInfo{
		model.RoleInvestigate: {Connector: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Effort: model.EffortHigh},
		model.RoleJudge:       {Connector: "judge", Provider: "openai", Model: "gpt-5.6", Temperature: new(0.0)},
	},
	Strategies:    map[string]string{"generic-alert": "1a2b3c4d"},
	PromptVersion: "2", JudgePromptVersion: JudgePromptVersion,
}

func TestReportMatchesSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// edit changes the decoded report before it is checked.
		edit    func(t *testing.T, doc map[string]any)
		wantErr string
	}{
		{name: "a scored run matches the schema"},
		{
			name:    "a field the schema does not know is caught",
			edit:    func(_ *testing.T, doc map[string]any) { doc["colour"] = "red" },
			wantErr: "colour",
		},
		{
			name: "a share above 1 is caught",
			edit: func(t *testing.T, doc map[string]any) {
				t.Helper()

				metrics, ok := doc["metrics"].(map[string]any)
				require.True(t, ok)
				metrics["overall"] = 1.5
			},
			wantErr: "maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := scoredResults(t)

			rep, err := results.Report(testInfo)
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, rep.WriteJSON(&out))

			var doc map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &doc))

			if tt.edit != nil {
				tt.edit(t, doc)
			}

			err = reportSchema(t).Validate(doc)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestReadReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// content is the file; WRITTEN means the report of scoredResults.
		content string
		wantErr string
	}{
		{name: "a report this Sidekick wrote", content: "WRITTEN"},
		{name: "another schema version", content: `{"schemaVersion": 2}`, wantErr: "has schema version 2; this Sidekick reads version 1"},
		{name: "not JSON", content: "nope", wantErr: "invalid character"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content := []byte(tt.content)

			var written *JSONReport

			if tt.content == "WRITTEN" {
				var err error

				written, err = scoredResults(t).Report(testInfo)
				require.NoError(t, err)

				var out bytes.Buffer
				require.NoError(t, written.WriteJSON(&out))
				content = out.Bytes()
			}

			path := filepath.Join(t.TempDir(), "report.json")
			require.NoError(t, os.WriteFile(path, content, 0o600))

			got, err := ReadReport(path)
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrInvalidReport)
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, written.Metrics, got.Metrics)
			require.Equal(t, written.Intervals, got.Intervals)
			require.Equal(t, written.Scenarios, got.Scenarios)
			require.Equal(t, written.RunInfo, got.RunInfo)
		})
	}
}

func TestReportBeforeScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(r *Results) error
	}{
		{name: "report.json", write: func(r *Results) error { _, err := r.Report(testInfo); return err }},
		{name: "the Markdown summary", write: func(r *Results) error { return r.WriteMarkdown(&bytes.Buffer{}, testInfo) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.write(&Results{}), errNotScored)
		})
	}
}

func TestWriteJUnit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want []string
	}{
		{
			name: "one suite per scenario and one case per run",
			want: []string{
				`<testsuites name="sidekick bench" tests="5" failures="3">`,
				`<testsuite name="pool" tests="3" failures="2" time="6.000">`,
				`<testcase classname="pool" name="run 1" time="1.000">`,
				`<failure message="the root cause does not match: score 0.60, every required item met: false">`,
				`<failure message="the verdict is &#34;inconclusive&#34;; expected true_positive">`,
				`<testsuite name="judge-error" tests="1" failures="1" time="1.000">`,
				`the judge gave no grades: no grades`,
				`<testsuite name="no-rubric" tests="1" failures="0" time="1.000">`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, scoredResults(t).WriteJUnit(&out))
			require.True(t, strings.HasPrefix(out.String(), xml.Header))

			for _, want := range tt.want {
				require.Contains(t, out.String(), want)
			}

			var parsed junitSuites
			require.NoError(t, xml.Unmarshal(out.Bytes(), &parsed))
			require.Len(t, parsed.Suites, 3)
		})
	}
}

func TestWriteMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want []string
	}{
		{
			name: "the totals and one row per scenario",
			want: []string{
				"## Sidekick benchmark\n",
				"Sidekick v0.1.0 (abc1234), 3 scenarios, 3 runs each, investigated by claude (claude-opus-5-5), judged by judge (gpt-5.6). Prompt 2, judge prompt " + JudgePromptVersion + ".\n",
				"| Metric | Value | 95% interval |\n|---|---|---|\n",
				"| verdict accuracy | 0.80 | ",
				"| false positive precision | - | - |\n",
				"| runs | 5 | |\n",
				"| Scenario | Correct | Root cause | Median cost | Median time |\n",
				"| pool | 2/3 | 0.73 ± 0.23 | $0.0020 | 2s |\n",
				"| no-rubric | 1/1 | - | $0.0010 | 1s |\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, scoredResults(t).WriteMarkdown(&out, testInfo))

			for _, want := range tt.want {
				require.Contains(t, out.String(), want)
			}
		})
	}
}

// scoredResults returns scored results of three scenarios: pool, with a
// rubric, evidence checks and an unavailable source, and three runs
// graded 1, 0.6 and 0.6; judge-error, whose one run the judge gives no
// grades for; and no-rubric, with one run and nothing to grade.
func scoredResults(t *testing.T) *Results {
	t.Helper()

	grader := NewMockGrader(gomock.NewController(t))
	gomock.InOrder(
		grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit", Met: true}, {ID: "fix", Met: true}}, CostMicros: 50}, nil),
		grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit"}, {ID: "fix", Met: true}}, CostMicros: 50}, nil),
		grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit"}, {ID: "fix", Met: true}}, CostMicros: 50}, nil),
		grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{}, errors.New("no grades")),
	)

	pool := Expected{
		Verdict:             report.TruePositive,
		RootCause:           RootCause{Summary: "The pool ran out.", Rubric: poolRubric},
		Evidence:            []EvidenceCheck{{CalledTool: "github__list_commits"}, {CitedURL: commitURL}},
		UnavailableReported: []string{"k8s"},
	}

	results := &Results{
		StartedAt:  time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 10, 28, 9, 1, 0, 0, time.UTC),
		Repeat:     3,
		Scenarios: []ScenarioResult{
			{
				ID: "pool", Title: "Pool", Path: "bench/scenarios/sample/pool.yaml", Expected: pool,
				Runs: []RunResult{
					withCheck(withCitation(withTool(usageRun(1000, time.Second), "github__list_commits"), commitURL), "k8s", report.CheckUnavailable),
					withTool(usageRun(2000, 2*time.Second), "github__list_commits"),
					withVerdict(usageRun(3000, 3*time.Second), report.Inconclusive),
				},
			},
			{
				ID: "judge-error", Expected: Expected{Verdict: report.TruePositive, RootCause: RootCause{Rubric: poolRubric}},
				Runs: []RunResult{usageRun(1000, time.Second)},
			},
			scenario("no-rubric", report.TruePositive, usageRun(1000, time.Second)),
		},
	}

	for i := range results.Scenarios[0].Runs {
		results.Scenarios[0].Runs[i].Run = i + 1
	}

	require.NoError(t, Score(t.Context(), results, ScoreOptions{Grader: grader, Seed: DefaultSeed}))

	return results
}

// reportSchema returns the resolved schema of report.json.
func reportSchema(t *testing.T) *jsonschema.Resolved {
	t.Helper()

	raw, err := os.ReadFile(reportSchemaFile)
	require.NoError(t, err)

	var s jsonschema.Schema
	require.NoError(t, json.Unmarshal(raw, &s))

	resolved, err := s.Resolve(nil)
	require.NoError(t, err)

	return resolved
}

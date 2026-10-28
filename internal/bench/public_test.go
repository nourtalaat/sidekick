package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	_ "github.com/nourtalaat/sidekick/internal/intake/datadog" // Intake adapter "datadog", which a public scenario uses.
	_ "github.com/nourtalaat/sidekick/internal/intake/sentry"  // Intake adapter "sentry", which a public scenario uses.
	_ "github.com/nourtalaat/sidekick/internal/intake/signoz"  // Intake adapter "signoz", which a public scenario uses.
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// publicDir is the public scenario set.
var publicDir = filepath.Join(repoRoot, "bench", "scenarios", "public")

// triageTag marks the scenario that triage can end without a tool call.
const triageTag = "triage"

// builtinStrategies are the strategies the public set must use.
var builtinStrategies = []string{"generic-alert", "latency-alert", "error-rate-alert", "service-down"}

// mcpSources are the sources of type mcp that have a tool snapshot, a docs
// page and a public scenario.
var mcpSources = []string{"signoz", "datadog", "sentry", "linear"}

// allowBlock is the first YAML block of a docs page, the source's config.
var allowBlock = regexp.MustCompile("(?s)```yaml\n(.*?)```")

func TestPublicScenariosPassTheChecksOfNew(t *testing.T) {
	t.Parallel()

	scenarios := loadPublic(t)
	cfg, regs := loadTestConfig(t, modeltest.New(t, model.Capabilities{ToolCalling: true}))

	_, err := New(t.Context(), scenarios, Options{Config: cfg, Registries: regs})
	require.NoError(t, err)
}

func TestPublicScenariosAreComplete(t *testing.T) {
	t.Parallel()

	for _, sc := range loadPublic(t) {
		t.Run(sc.ID, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, strings.TrimSuffix(filepath.Base(sc.Path), ".yaml"), sc.ID, "the ID is the file name")
			require.NotEmpty(t, sc.Title)
			require.NotEmpty(t, sc.Tags)
			require.NotEmpty(t, sc.Strategy, "a public scenario names its strategy")
			require.NotEmpty(t, sc.Expected.RootCause.Summary)
			require.NotEmpty(t, sc.Expected.RootCause.Rubric, "a rubric for the judge")

			if !slices.Contains(sc.Tags, triageTag) {
				require.NotEmpty(t, sc.Expected.Evidence, "evidence checks")
			}
		})
	}
}

func TestPublicScenariosCoverTheSet(t *testing.T) {
	t.Parallel()

	scenarios := loadPublic(t)

	verdicts := map[report.Verdict]bool{}
	strategies := map[string]bool{}
	snapshots := map[string]bool{}

	var triageDuplicate, sourceFailure bool

	for _, sc := range scenarios {
		verdicts[sc.Expected.Verdict] = true
		strategies[sc.Strategy] = true

		for _, cfg := range sc.Sources {
			snapshots[filepath.Base(cfg.Tools.Snapshot)] = true
		}

		if slices.Contains(sc.Tags, triageTag) && sc.Expected.Verdict == report.Duplicate {
			triageDuplicate = true
		}

		for _, name := range sc.Expected.UnavailableReported {
			sourceFailure = sourceFailure || sc.Sources[name].Unavailable
		}
	}

	for _, v := range []report.Verdict{report.TruePositive, report.FalsePositive, report.Inconclusive, report.Duplicate, report.KnownIssue, report.Flapping} {
		require.True(t, verdicts[v], "no scenario expects the verdict %s", v)
	}

	for _, name := range builtinStrategies {
		require.True(t, strategies[name], "no scenario uses the strategy %s", name)
	}

	for _, name := range mcpSources {
		require.True(t, snapshots[name+".json"], "no scenario uses the %s snapshot", name)
	}

	require.True(t, triageDuplicate, "no triage scenario expects a duplicate")
	require.True(t, sourceFailure, "no scenario makes a source unavailable and expects the report to name it")
}

func TestMCPSnapshots(t *testing.T) {
	t.Parallel()

	for _, name := range mcpSources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			snapshot := readSnapshotNames(t, name)
			require.NotEmpty(t, snapshot.names)

			for _, tool := range snapshot.tools {
				_, err := source.QualifiedName(name, tool.Name)
				require.NoError(t, err, "tool %s", tool.Name)
				require.NotEmpty(t, tool.Description, "tool %s", tool.Name)

				_, err = tools.ParseInputSchema(tool.InputSchema)
				require.NoError(t, err, "the input schema of tool %s", tool.Name)
			}

			require.ElementsMatch(t, docsAllowList(t, name), snapshot.names,
				"docs/sources/%s.md allows the tools of the snapshot, no more and no fewer", name)
		})
	}
}

// snapshotTool is one tool of a snapshot file.
type snapshotTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// snapshotFile is a tool snapshot file, with the tool names it lists.
type snapshotFile struct {
	tools []snapshotTool
	names []string
}

// loadPublic loads the public scenarios.
func loadPublic(t *testing.T) []Scenario {
	t.Helper()

	scenarios, err := LoadScenarios(publicDir, LoadOptions{BaseDir: repoRoot})
	require.NoError(t, err)
	require.NotEmpty(t, scenarios)

	return scenarios
}

// readSnapshotNames reads the snapshot of the source called name.
func readSnapshotNames(t *testing.T, name string) snapshotFile {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot, "bench", "tools", name+".json"))
	require.NoError(t, err)

	var file struct {
		Tools []snapshotTool `json:"tools"`
	}

	require.NoError(t, json.Unmarshal(raw, &file))

	out := snapshotFile{tools: file.Tools}
	for _, tool := range file.Tools {
		out.names = append(out.names, tool.Name)
	}

	return out
}

// docsAllowList returns the tools.allow list in the config at the top of the
// docs page of the source called name.
func docsAllowList(t *testing.T, name string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot, "docs", "sources", name+".md"))
	require.NoError(t, err)

	match := allowBlock.FindSubmatch(raw)
	require.NotNil(t, match, "docs/sources/%s.md has no YAML block", name)

	var doc struct {
		Sources map[string]struct {
			Type  string `yaml:"type"`
			Tools struct {
				Allow []string `yaml:"allow"`
			} `yaml:"tools"`
		} `yaml:"sources"`
	}

	require.NoError(t, yaml.Unmarshal(match[1], &doc))
	require.Contains(t, doc.Sources, name)
	require.Equal(t, "mcp", doc.Sources[name].Type)

	return doc.Sources[name].Tools.Allow
}

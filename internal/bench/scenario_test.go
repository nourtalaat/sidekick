package bench

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// repoRoot is the directory the sample scenario's snapshot paths are
// relative to.
const repoRoot = "../.."

// sampleScenario is the sample scenario shipped in the repository.
var sampleScenario = filepath.Join(repoRoot, "bench", "scenarios", "sample", "checkout-pool-size.yaml")

// minimal is the smallest valid scenario, for cases that change one part.
const minimal = `
id: minimal
now: 2026-05-04T10:20:00Z
event:
  adapter: alertmanager
  payload: { version: "4", alerts: [] }
sources:
  k8s: { unavailable: true }
expected:
  verdict: false_positive
`

func TestLoadScenariosSample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "the file", path: sampleScenario},
		{name: "its directory", path: filepath.Dir(sampleScenario)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := LoadScenarios(tt.path, LoadOptions{BaseDir: repoRoot})
			require.NoError(t, err)
			require.Len(t, got, 1)

			sc := got[0]
			require.Equal(t, "checkout-pool-size", sc.ID)
			require.Equal(t, time.Date(2026, 5, 4, 10, 20, 0, 0, time.UTC), sc.Now)
			require.Equal(t, []string{"github", "k8s"}, sc.SourceNames())
			require.Equal(t, report.TruePositive, sc.Expected.Verdict)
			require.Equal(t, []string{"k8s"}, sc.Expected.UnavailableReported)
			require.Equal(t, Limits{MaxToolCalls: 30, MaxCostUSD: 0.5}, sc.Limits)
			require.Equal(t, sampleScenario, sc.Path)

			github := sc.SourceConfig("github")
			require.Equal(t, filepath.Join(repoRoot, "bench", "tools", "github.json"), github.Tools.Snapshot)
			require.Equal(t, sc.Now, github.Now, "the source gets the scenario clock")
			require.Equal(t, mock.DefaultNoData, github.Default, "the source gets the scenario default")
			require.Len(t, github.Fixtures, 4)
			require.True(t, sc.SourceConfig("k8s").Unavailable)

			body, err := sc.Body()
			require.NoError(t, err)
			require.Contains(t, string(body), `"startsAt":"2026-05-04T10:12:00Z"`)
		})
	}
}

func TestLoadScenariosReadsRouteDetails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := minimal + "route: { details: { owner: payments team, environment: prod } }\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(content), 0o600))

	got, err := LoadScenarios(dir, LoadOptions{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, map[string]string{"owner": "payments team", "environment": "prod"}, got[0].Route.Details)

	r := &Runner{}
	require.Equal(t, got[0].Route.Details, r.route(got[0]).Details, "the run's route has the details")
}

func TestLoadScenariosProblems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// files are written to a new directory, by name.
		files map[string]string
		// want are lines the error must hold, after the file name.
		want []string
	}{
		{name: "empty file", files: map[string]string{"a.yaml": ""}, want: []string{"a.yaml: the file is empty"}},
		{name: "bad YAML", files: map[string]string{"a.yaml": "id: [\n"}, want: []string{"a.yaml: invalid YAML"}},
		{name: "two documents", files: map[string]string{"a.yaml": minimal + "---\nid: b\n"}, want: []string{"a.yaml: the file must hold one YAML document"}},
		{name: "unknown key", files: map[string]string{"a.yaml": minimal + "colour: red\n"}, want: []string{"a.yaml: colour: unknown"}},
		{
			name:  "missing fields",
			files: map[string]string{"a.yaml": "title: nothing\n"},
			want: []string{
				"a.yaml: id: required", "a.yaml: now: required", "a.yaml: event.adapter: required",
				"a.yaml: event.payload: required", "a.yaml: sources: required", "a.yaml: expected.verdict: required",
			},
		},
		{
			name:  "bad values",
			files: map[string]string{"a.yaml": badValues},
			want: []string{
				`a.yaml: id: "Bad ID" must be lower-case`,
				"a.yaml: route.mode: read-write is not allowed in a scenario",
				"a.yaml: sources.K8s: the name must be lower-case",
				"a.yaml: sources.github.tools.snapshot: required unless unavailable is true",
				"a.yaml: sources.github.fixtures[0].match.args.q: regex:",
				`a.yaml: default: must be no_data or error, not "nothing"`,
				`a.yaml: expected.verdict: unknown verdict "real"`,
				`a.yaml: expected.acceptable[0]: unknown verdict "maybe"`,
				`a.yaml: expected.rootCause.rubric[1].id: "pool" is used twice`,
				"a.yaml: expected.rootCause.rubric[1].weight: must be more than 0",
				"a.yaml: expected.evidence[0]: set calledTool or citedURL, not both",
				`a.yaml: expected.unavailableReported[0]: "signoz" is not a source of the scenario`,
				"a.yaml: limits.maxToolCalls: must not be negative",
			},
		},
		{
			name:  "bad route details",
			files: map[string]string{"a.yaml": minimal + "route: { details: { owner: \"a\\nb\", \"on call\": x } }\n"},
			want: []string{
				`a.yaml: route.details: key "on call" must be 1 to 64 characters`,
				`a.yaml: route.details: key "owner": the value must be one line`,
			},
		},
		{
			name:  "a value of the wrong type",
			files: map[string]string{"a.yaml": minimal + "limits: { maxWallTime: soon }\n"},
			want:  []string{"a.yaml: limits.maxWallTime:"},
		},
		{
			name:  "the same id twice",
			files: map[string]string{"a.yaml": minimal, "b.yml": minimal},
			want:  []string{`b.yml: id: "minimal" is also the id of`},
		},
		{
			name:  "every file is checked",
			files: map[string]string{"a.yaml": "id: a\n", "b.yaml": "id: b\n", "notes.txt": "not a scenario"},
			want:  []string{"a.yaml: now: required", "b.yaml: now: required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for name, content := range tt.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}

			_, err := LoadScenarios(dir, LoadOptions{})
			require.ErrorIs(t, err, ErrInvalidScenario)

			for _, want := range tt.want {
				require.ErrorContains(t, err, dir+string(filepath.Separator)+want)
			}
		})
	}
}

func TestLoadScenariosPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string
	}{
		{
			name: "missing path",
			path: func(t *testing.T) string {
				t.Helper()

				return filepath.Join(t.TempDir(), "missing")
			},
			wantErr: "no such file",
		},
		{
			name: "directory without scenarios",
			path: func(t *testing.T) string {
				t.Helper()

				return t.TempDir()
			},
			wantErr: "no .yaml or .yml files under",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadScenarios(tt.path(t), LoadOptions{})
			require.ErrorIs(t, err, ErrInvalidScenario)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// badValues is a scenario with a bad value in most fields.
const badValues = `
id: Bad ID
now: 2026-05-04T10:20:00Z
route: { mode: read-write }
event:
  adapter: alertmanager
  payload: { version: "4" }
sources:
  K8s: { unavailable: true }
  github:
    fixtures:
      - tool: search
        match: { args: { q: { regex: "(" } } }
        respond: { text: x }
default: nothing
expected:
  verdict: real
  acceptable: [maybe]
  rootCause:
    rubric:
      - { id: pool, text: Names the pool, weight: 1 }
      - { id: pool, text: Names it again, weight: 0 }
  evidence:
    - { calledTool: a, citedURL: "https://b" }
  unavailableReported: [signoz]
limits: { maxToolCalls: -1 }
`

func TestParseScenario(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr []string
	}{
		{name: "minimal", in: minimal},
		{name: "unknown key", in: minimal + "colour: red\n", wantErr: []string{"colour"}},
		{name: "no id", in: "now: 2026-05-04T10:20:00Z\n", wantErr: []string{"id: required", "event.adapter: required"}},
		{name: "empty", in: "", wantErr: []string{"the file is empty"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseScenario([]byte(tt.in))
			if tt.wantErr == nil {
				require.NoError(t, err)
				require.Equal(t, "minimal", got.ID)

				return
			}

			require.ErrorIs(t, err, ErrInvalidScenario)

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

// approvedScenario is minimal with approved mode and one mock action.
const approvedScenario = minimal + `
route: { mode: approved, actions: [rollback] }
actions:
  rollback:
    spec: { snapshot: bench/actions/kubernetes.rollback.json }
`

func TestParseScenarioActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		// wantErr are the lines the error must hold. Empty means valid.
		wantErr []string
	}{
		{name: "approved mode with a mock action", in: approvedScenario},
		{name: "read-only mode with no actions", in: minimal + "route: { mode: read-only }\n"},
		{
			name:    "read-write is refused",
			in:      minimal + "route: { mode: read-write }\n",
			wantErr: []string{"route.mode: read-write is not allowed in a scenario"},
		},
		{
			name:    "an unknown mode",
			in:      minimal + "route: { mode: auto }\n",
			wantErr: []string{`route.mode: unknown mode "auto"`},
		},
		{
			name:    "approved mode with no actions",
			in:      minimal + "route: { mode: approved }\n",
			wantErr: []string{"route.actions: list at least one action"},
		},
		{
			name: "actions without approved mode",
			in:   minimal + "route: { actions: [rollback] }\nactions:\n  rollback: { spec: { snapshot: s.json } }\n",
			wantErr: []string{
				"route.actions: must be empty unless route.mode is approved",
				"actions: need route.mode approved",
			},
		},
		{
			name: "a route action that is not an action of the scenario",
			in:   minimal + "route: { mode: approved, actions: [rollback, restart, restart] }\nactions:\n  rollback: { spec: { snapshot: s.json } }\n",
			wantErr: []string{
				`route.actions[1]: "restart" is not an action of the scenario`,
				`route.actions[2]: "restart" is listed twice`,
			},
		},
		{
			name:    "an action the route does not list",
			in:      minimal + "route: { mode: approved, actions: [rollback] }\nactions:\n  rollback: { spec: { snapshot: s.json } }\n  restart: { spec: { snapshot: s.json } }\n",
			wantErr: []string{"actions.restart: not listed in route.actions"},
		},
		{
			name: "a bad action name and a missing snapshot",
			in:   minimal + "route: { mode: approved, actions: [Roll_Back] }\nactions:\n  Roll_Back: { spec: {} }\n",
			wantErr: []string{
				"actions.Roll_Back: the name must start with a lower-case letter",
				"actions.Roll_Back.spec.snapshot: required",
			},
		},
		{
			name:    "an unknown key of an action",
			in:      minimal + "route: { mode: approved, actions: [rollback] }\nactions:\n  rollback: { spec: { snapshot: s.json }, colour: red }\n",
			wantErr: []string{"colour"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseScenario([]byte(tt.in))
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidScenario)

			for _, want := range tt.wantErr {
				require.ErrorContains(t, err, want)
			}

			require.Empty(t, got.ID)
		})
	}
}

func TestParseScenarioKeepsActions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "the route's list and the action's snapshot", in: approvedScenario},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sc, err := ParseScenario([]byte(tt.in))
			require.NoError(t, err)
			require.Equal(t, action.Approved, sc.Route.Mode)
			require.Equal(t, []string{"rollback"}, sc.Route.Actions)
			require.Equal(t, []string{"rollback"}, sc.ActionNames())
			require.Equal(t, "bench/actions/kubernetes.rollback.json", sc.Actions["rollback"].Spec.Snapshot)

			sc.ResolveSnapshots("/repo")
			require.Equal(t, filepath.Join("/repo", "bench", "actions", "kubernetes.rollback.json"), sc.Actions["rollback"].Spec.Snapshot)
		})
	}
}

func TestScenarioMarshalRoundTrip(t *testing.T) {
	t.Parallel()

	loaded, err := LoadScenarios(sampleScenario, LoadOptions{})
	require.NoError(t, err)

	want := loaded[0]
	want.Path = ""

	raw, err := yaml.Marshal(want)
	require.NoError(t, err)

	got, err := ParseScenario(raw)
	require.NoError(t, err, string(raw))
	require.Equal(t, want, got)
}

func TestScenarioMarshalLeavesOutEmptyFields(t *testing.T) {
	t.Parallel()

	sc, err := ParseScenario([]byte(minimal))
	require.NoError(t, err)

	raw, err := yaml.Marshal(sc)
	require.NoError(t, err)

	for _, key := range []string{"tags:", "strategy:", "route:", "default:", "limits:", "config:", "actions:", "acceptable:", "rubric:", "evidence:"} {
		require.NotContains(t, string(raw), key)
	}
}

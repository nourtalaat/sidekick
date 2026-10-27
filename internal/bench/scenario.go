package bench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/mock"
)

// ErrInvalidScenario is wrapped by every error about the content of a
// scenario file, so the command can exit with ExitBadInput.
var ErrInvalidScenario = errors.New("invalid scenario")

// scenarioExtensions are the file extensions LoadScenarios reads in a
// directory.
var scenarioExtensions = []string{".yaml", ".yml"}

// idPattern is what a scenario ID may look like.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// verdicts are the verdicts expected.verdict and expected.acceptable may
// name.
var verdicts = []report.Verdict{
	report.TruePositive, report.FalsePositive, report.Inconclusive,
	report.Duplicate, report.KnownIssue, report.Flapping,
}

// Scenario is one benchmark scenario file: an event, the mock sources the
// investigation may call, and the outcome a good investigation reaches.
type Scenario struct {
	// ID names the scenario in results. It is unique in a run.
	ID    string   `yaml:"id"`
	Title string   `yaml:"title"`
	Tags  []string `yaml:"tags"`
	// Strategy is the strategy the investigation uses. Empty means the
	// default strategy.
	Strategy string `yaml:"strategy"`
	// Now is the scenario clock: the time the event arrives at, and the
	// time within matchers count from.
	Now time.Time `yaml:"now"`
	// Route holds the route settings of the run.
	Route Route `yaml:"route"`
	// Event is the payload that opens the investigation.
	Event Event `yaml:"event"`
	// Sources are the mock sources, by source name.
	Sources map[string]mock.Config `yaml:"sources"`
	// Default is what a call no fixture matches returns, for every source
	// that does not set its own. Empty means mock.DefaultNoData.
	Default mock.Default `yaml:"default"`
	// Expected is the outcome the run is scored against.
	Expected Expected `yaml:"expected"`
	// Limits bound each run of the scenario. Zero values take the
	// config's per-investigation limits.
	Limits Limits `yaml:"limits"`

	// Path is the file the scenario was read from.
	Path string `yaml:"-"`
}

// Route holds the route settings of a scenario's run.
type Route struct {
	// Mode is the action mode. Only read-only exists until M4.
	Mode action.Mode `yaml:"mode"`
}

// Event is a scenario's payload and the adapter that turns it into an
// event, as a webhook delivery is.
type Event struct {
	// Adapter is the intake adapter type, for example alertmanager.
	Adapter string `yaml:"adapter"`
	// Config is the adapter's own settings, as they would be under
	// intake.webhooks.<name> in sidekick.yaml. Most adapters have none.
	Config yaml.Node `yaml:"config"`
	// Payload is the webhook body, written as YAML. It is sent as JSON.
	Payload any `yaml:"payload"`
}

// Limits bound one run, as a route's budgets do.
type Limits struct {
	MaxTokens    int           `yaml:"maxTokens"`
	MaxToolCalls int           `yaml:"maxToolCalls"`
	MaxWallTime  time.Duration `yaml:"maxWallTime"`
	MaxCostUSD   float64       `yaml:"maxCostUSD"`
}

// Expected is the outcome a good investigation of the scenario reaches.
type Expected struct {
	// Verdict is the right verdict.
	Verdict report.Verdict `yaml:"verdict"`
	// Acceptable are other verdicts that still count as correct.
	Acceptable []report.Verdict `yaml:"acceptable"`
	// RootCause is the cause the report should find, with the rubric it is
	// graded by.
	RootCause RootCause `yaml:"rootCause"`
	// Evidence are checks of the run made in code, without a judge.
	Evidence []EvidenceCheck `yaml:"evidence"`
	// UnavailableReported are the sources the report must list as
	// unavailable in checked.
	UnavailableReported []string `yaml:"unavailableReported"`
}

// RootCause is the expected cause and its rubric.
type RootCause struct {
	Summary string       `yaml:"summary"`
	Rubric  []RubricItem `yaml:"rubric"`
}

// RubricItem is one point a judge grades as met or not met.
type RubricItem struct {
	ID     string  `yaml:"id"`
	Text   string  `yaml:"text"`
	Weight float64 `yaml:"weight"`
	// Required items must be met for the root cause to match.
	Required bool `yaml:"required"`
}

// EvidenceCheck is one check of the run. Exactly one field is set.
type EvidenceCheck struct {
	// CalledTool is a tool the run must call, as the model names it, for
	// example github__list_commits.
	CalledTool string `yaml:"calledTool"`
	// CitedURL is a URL the report must cite.
	CitedURL string `yaml:"citedURL"`
}

// LoadOptions change how scenarios are read.
type LoadOptions struct {
	// BaseDir is the directory relative tool snapshot paths are read
	// from. Empty means the working directory.
	BaseDir string
}

// SourceConfig returns the config of the scenario's source called name,
// with the scenario clock and the scenario's default filled in.
func (s Scenario) SourceConfig(name string) mock.Config {
	cfg := s.Sources[name]
	cfg.Now = s.Now

	if cfg.Default == "" {
		cfg.Default = s.Default
	}

	return cfg
}

// LoadScenarios reads the scenario file at path, or every .yaml and .yml
// file under the directory at path, in name order, and checks each one.
// Problems in the files give one error that wraps ErrInvalidScenario and
// lists every problem, one per line, as "<file>: <field>: <problem>".
func LoadScenarios(path string, opts LoadOptions) ([]Scenario, error) {
	files, err := findFiles(path, scenarioExtensions)
	if err != nil {
		return nil, fmt.Errorf("find the scenario files: %w", err)
	}

	var (
		out      []Scenario
		problems []string
	)

	seen := map[string]string{}

	for _, file := range files {
		sc, errs := loadScenario(file, opts)
		if len(errs) > 0 {
			problems = append(problems, errs...)

			continue
		}

		if prev, ok := seen[sc.ID]; ok {
			problems = append(problems, fmt.Sprintf("%s: id: %q is also the id of %s", file, sc.ID, prev))

			continue
		}

		seen[sc.ID] = file
		out = append(out, sc)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n%s", ErrInvalidScenario, strings.Join(problems, "\n"))
	}

	return out, nil
}

// loadScenario reads and checks one scenario file. It returns the
// problems as "<file>: <field>: <problem>".
func loadScenario(file string, opts LoadOptions) (Scenario, []string) {
	raw, err := os.ReadFile(file) //nolint:gosec // The user chooses the scenario files.
	if err != nil {
		return Scenario{}, []string{fmt.Sprintf("%s: %v", file, err)}
	}

	var sc Scenario

	problems := decodeStrict(raw, &sc)
	if len(problems) == 0 {
		problems = sc.validate()
	}

	if len(problems) > 0 {
		out := make([]string, len(problems))
		for i, p := range problems {
			out[i] = file + ": " + p
		}

		return Scenario{}, out
	}

	sc.Path = file
	sc.resolveSnapshots(opts.BaseDir)

	return sc, nil
}

// validate checks the decoded scenario and returns every problem as
// "<field>: <problem>".
func (s Scenario) validate() []string {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch {
	case s.ID == "":
		add("id: required")
	case !idPattern.MatchString(s.ID):
		add("id: %q must be lower-case letters, digits, '.', '_' and '-', at most 100 of them", s.ID)
	}

	if s.Now.IsZero() {
		add("now: required")
	}

	if s.Route.Mode != "" && s.Route.Mode != action.ReadOnly {
		add("route.mode: mode %q is not available yet: runs are read-only until action modes arrive in M4", s.Route.Mode)
	}

	if s.Event.Adapter == "" {
		add("event.adapter: required")
	}

	if s.Event.Payload == nil {
		add("event.payload: required")
	} else if _, err := s.Body(); err != nil {
		add("event.payload: cannot be sent as JSON: %v", err)
	}

	if len(s.Sources) == 0 {
		add("sources: required")
	}

	for _, name := range s.SourceNames() {
		if _, err := source.QualifiedName(name, "tool"); err != nil {
			add("sources.%s: the name must be lower-case letters, digits and '-', start with a letter, and have at most 24 characters", name)
		}

		if err := s.Sources[name].Validate(); err != nil {
			for line := range strings.Lines(err.Error()) {
				add("sources.%s.%s", name, strings.TrimSuffix(line, "\n"))
			}
		}
	}

	if !slices.Contains([]mock.Default{"", mock.DefaultNoData, mock.DefaultError}, s.Default) {
		add("default: must be %s or %s, not %q", mock.DefaultNoData, mock.DefaultError, s.Default)
	}

	problems = append(problems, s.Expected.validate(s.Sources)...)
	problems = append(problems, s.Limits.validate()...)

	return problems
}

// SourceNames returns the names of the scenario's sources, sorted.
func (s Scenario) SourceNames() []string {
	return slices.Sorted(maps.Keys(s.Sources))
}

// Body returns the event payload as the JSON a webhook delivery carries.
func (s Scenario) Body() ([]byte, error) {
	body, err := json.Marshal(s.Event.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode the payload of scenario %s as JSON: %w", s.ID, err)
	}

	return body, nil
}

// resolveSnapshots makes each relative snapshot path relative to baseDir.
func (s *Scenario) resolveSnapshots(baseDir string) {
	if baseDir == "" {
		return
	}

	for name, cfg := range s.Sources {
		if cfg.Tools.Snapshot != "" && !filepath.IsAbs(cfg.Tools.Snapshot) {
			cfg.Tools.Snapshot = filepath.Join(baseDir, cfg.Tools.Snapshot)
			s.Sources[name] = cfg
		}
	}
}

// validate checks the expected outcome against the scenario's sources.
func (e Expected) validate(sources map[string]mock.Config) []string {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch {
	case e.Verdict == "":
		add("expected.verdict: required")
	case !slices.Contains(verdicts, e.Verdict):
		add("expected.verdict: unknown verdict %q", e.Verdict)
	}

	for i, v := range e.Acceptable {
		if !slices.Contains(verdicts, v) {
			add("expected.acceptable[%d]: unknown verdict %q", i, v)
		}
	}

	problems = append(problems, validateRubric("expected.rootCause.rubric", e.RootCause.Rubric)...)

	for i, check := range e.Evidence {
		if (check.CalledTool == "") == (check.CitedURL == "") {
			add("expected.evidence[%d]: set calledTool or citedURL, not both", i)
		}
	}

	for i, name := range e.UnavailableReported {
		if _, ok := sources[name]; !ok {
			add("expected.unavailableReported[%d]: %q is not a source of the scenario", i, name)
		}
	}

	return problems
}

// validateRubric checks the rubric at path: each item has a unique ID, a
// text and a weight above 0.
func validateRubric(path string, rubric []RubricItem) []string {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	ids := map[string]bool{}

	for i, item := range rubric {
		p := fmt.Sprintf("%s[%d]", path, i)

		switch {
		case item.ID == "":
			add("%s.id: required", p)
		case ids[item.ID]:
			add("%s.id: %q is used twice", p, item.ID)
		}

		ids[item.ID] = true

		if item.Text == "" {
			add("%s.text: required", p)
		}

		if item.Weight <= 0 {
			add("%s.weight: must be more than 0", p)
		}
	}

	return problems
}

// validate checks that no limit is negative.
func (l Limits) validate() []string {
	var problems []string

	for _, f := range []struct {
		key      string
		negative bool
	}{
		{"maxTokens", l.MaxTokens < 0},
		{"maxToolCalls", l.MaxToolCalls < 0},
		{"maxWallTime", l.MaxWallTime < 0},
		{"maxCostUSD", l.MaxCostUSD < 0},
	} {
		if f.negative {
			problems = append(problems, "limits."+f.key+": must not be negative")
		}
	}

	return problems
}

// decodeStrict decodes one YAML document into target strictly: an unknown
// key is a problem. Scenario files and bench.yaml are read with it.
func decodeStrict(raw []byte, target any) []string {
	dec := yaml.NewDecoder(bytes.NewReader(raw))

	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return []string{"the file is empty"}
		}

		return []string{"invalid YAML: " + strings.TrimPrefix(err.Error(), "yaml: ")}
	}

	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return []string{"the file must hold one YAML document"}
	}

	err := config.Section{Node: &doc}.Decode(target)
	if err == nil {
		return nil
	}

	var decodeErrs config.Errors
	if !errors.As(err, &decodeErrs) {
		return []string{err.Error()}
	}

	problems := make([]string, len(decodeErrs))
	for i, e := range decodeErrs {
		problems[i] = e.String()
	}

	return problems
}

// findFiles returns path when it is a file, or the files under it with
// one of the extensions exts, in name order, when it is a directory.
// Problems give an error that wraps ErrInvalidScenario.
func findFiles(path string, exts []string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidScenario, err)
	}

	if !info.IsDir() {
		return []string{path}, nil
	}

	var files []string

	err = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", p, err)
		}

		if !entry.IsDir() && slices.Contains(exts, filepath.Ext(p)) {
			files = append(files, p)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidScenario, err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no %s files under %s", ErrInvalidScenario, strings.Join(exts, " or "), path)
	}

	return files, nil
}

// Package strategy loads investigation strategies: the built-in ones that
// ship in the binary and the file ones read from strategies.dir.
//
// A strategy tells the investigation what to check first, the common causes
// and the signs of a false positive for one kind of event. Strategies are
// YAML files, decoded strictly: an unknown key is an error. File strategies
// are read-only; Sidekick never writes them. Runtime strategies, stored in
// Postgres with versions, come in a later milestone.
package strategy

import (
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// GenericAlert is the built-in strategy a route uses when it names none.
const GenericAlert = "generic-alert"

// namePattern is the rule for strategy names.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// nameRule explains namePattern in error messages.
const nameRule = `must start with a lower-case letter and hold only lower-case letters, digits and "-", at most 64 characters`

// Origin says where a strategy was loaded from.
type Origin string

// Origins.
const (
	// BuiltIn strategies ship in the binary.
	BuiltIn Origin = "built-in"
	// File strategies come from strategies.dir.
	File Origin = "file"
)

// Strategy is one investigation strategy.
type Strategy struct {
	// Name identifies the strategy; routes refer to it by name.
	Name string `yaml:"name"`
	// Description says which events the strategy is for.
	Description string `yaml:"description"`
	// Match describes the events the strategy fits. Triage uses it to pick
	// a strategy, which arrives with the triage role; until then a route
	// names its strategy and Match is only checked.
	Match Match `yaml:"match"`
	// Sources lists the sources worth trying first.
	Sources Sources `yaml:"sources"`
	// First lists the checks to run first, in order.
	First []string `yaml:"first"`
	// CommonCauses lists the usual causes of this kind of event.
	CommonCauses []string `yaml:"commonCauses"`
	// FalsePositiveSigns lists signs that the event is a false positive.
	FalsePositiveSigns []string `yaml:"falsePositiveSigns"`
	// VerdictHints add advice for particular verdicts.
	VerdictHints VerdictHints `yaml:"verdictHints"`
	// Budgets limit investigations that use the strategy. A limit of zero
	// means the strategy sets none. Where both set one, the lower of the
	// route's and the strategy's limits applies; the agent loop enforces it.
	Budgets Budgets `yaml:"budgets"`
	// Watchers are checks started for event groups investigated with this
	// strategy. Watchers arrive in M1; until then they are only checked for
	// shape and never started.
	Watchers []Watcher `yaml:"watchers"`
	// WatcherDefaults are the defaults for this strategy's watchers and for
	// watchers started during its investigations. Like Watchers, they are
	// only checked for shape until M1.
	WatcherDefaults *WatcherSettings `yaml:"watcherDefaults"`

	// Version is a hash of the file content, set when the strategy is
	// loaded. Investigations record it, so results always refer to an exact
	// version.
	Version string `yaml:"-"`
	// Origin says where the strategy was loaded from.
	Origin Origin `yaml:"-"`
	// Path is the file the strategy was loaded from. For a built-in
	// strategy it is the file name inside the binary.
	Path string `yaml:"-"`
}

// Match describes the events a strategy fits.
type Match struct {
	// AlertNames are globs, in path.Match syntax, matched against the alert
	// name.
	AlertNames []string `yaml:"alertNames"`
	// TextPattern is a regular expression matched against the event text.
	TextPattern string `yaml:"textPattern"`
}

// Sources holds a strategy's source preferences.
type Sources struct {
	// Prefer lists source names to try first. Names that are not
	// configured are skipped, so a shared strategy can name sources a
	// deployment does not have.
	Prefer []string `yaml:"prefer"`
}

// VerdictHints hold advice for each verdict. Empty hints are left out of
// the prompt.
type VerdictHints struct {
	TruePositive  string `yaml:"truePositive"`
	FalsePositive string `yaml:"falsePositive"`
	Inconclusive  string `yaml:"inconclusive"`
	Duplicate     string `yaml:"duplicate"`
	KnownIssue    string `yaml:"knownIssue"`
	Flapping      string `yaml:"flapping"`
}

// Budgets are a strategy's limits for one investigation. The fields match
// the per-investigation route budgets.
type Budgets struct {
	MaxTokens    int           `yaml:"maxTokens"`
	MaxToolCalls int           `yaml:"maxToolCalls"`
	MaxWallTime  time.Duration `yaml:"maxWallTime"`
	MaxCostUSD   float64       `yaml:"maxCostUSD"`
}

// Watcher is a periodic, read-only check that reports when the affected
// service is back up. Not used until M1.
type Watcher struct {
	// Name is unique within the strategy.
	Name string `yaml:"name"`
	// Match selects the events the watcher starts for. Empty matches every
	// event.
	Match WatcherMatch `yaml:"match"`
	// Check is what the watcher runs.
	Check WatcherCheck `yaml:"check"`

	WatcherSettings `yaml:",inline"`
}

// WatcherSettings are the timing settings of a watcher. Zero means the
// value comes from the next level of defaults.
type WatcherSettings struct {
	// Interval is the time between checks.
	Interval time.Duration `yaml:"interval"`
	// Timeout is how long the watcher may run without recovering.
	Timeout time.Duration `yaml:"timeout"`
	// PassesToRecover is the number of passing checks in a row that count
	// as recovered.
	PassesToRecover int `yaml:"passesToRecover"`
	// CheckTimeout is the time limit of one check.
	CheckTimeout time.Duration `yaml:"checkTimeout"`
}

// WatcherMatch selects events for a watcher. Every field that is set must
// match.
type WatcherMatch struct {
	// AlertName lists globs matched against the alert name.
	AlertName []string `yaml:"alertName"`
	// Text is a regular expression matched against the event text.
	Text string `yaml:"text"`
}

// CheckType is the kind of a watcher check.
type CheckType string

// Check types.
const (
	CheckHTTP  CheckType = "http"
	CheckTool  CheckType = "tool"
	CheckAlert CheckType = "alert"
)

// knownCheckTypes lists the check types, for checks and messages.
var knownCheckTypes = []CheckType{CheckHTTP, CheckTool, CheckAlert}

// WatcherCheck is one watcher check. Which fields apply depends on Type.
type WatcherCheck struct {
	Type CheckType `yaml:"type"`

	// URL is fetched with GET by an http check.
	URL string `yaml:"url"`
	// Expect is what a passing http response looks like.
	Expect *Expect `yaml:"expect"`
	// Headers are request headers of an http check, with values read from
	// secrets.
	Headers map[string]SecretRef `yaml:"headers"`
	// Timeout overrides the check timeout for this check.
	Timeout time.Duration `yaml:"timeout"`

	// Source and Tool name the tool a tool check calls, as the source
	// names it.
	Source string `yaml:"source"`
	Tool   string `yaml:"tool"`
	// Args are the fixed arguments of the tool call, any JSON value.
	Args yaml.Node `yaml:"args"`
	// Condition decides whether a tool result passes.
	Condition *Condition `yaml:"condition"`
	// NoData says whether a result without data passes or fails.
	NoData string `yaml:"noData"`

	// Require is "all" or "any" for an alert check.
	Require string `yaml:"require"`
}

// Expect is what a passing http check response looks like.
type Expect struct {
	Status    []int  `yaml:"status"`
	BodyRegex string `yaml:"bodyRegex"`
}

// SecretRef names a key of a mounted secret.
type SecretRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

// Condition decides whether a tool check passes: a JSON path with a
// comparison, or a regular expression on the result text.
type Condition struct {
	JSONPath  string    `yaml:"jsonPath"`
	Op        string    `yaml:"op"`
	Value     yaml.Node `yaml:"value"`
	TextRegex string    `yaml:"textRegex"`
	Negate    bool      `yaml:"negate"`
}

// validate checks the rules decoding cannot, and returns every problem.
func (s *Strategy) validate() []string {
	var problems []string

	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch {
	case s.Name == "":
		add("name: required")
	case !namePattern.MatchString(s.Name):
		add("name: %q %s", s.Name, nameRule)
	}

	if s.Description == "" {
		add("description: required")
	}

	for i, g := range s.Match.AlertNames {
		if _, err := path.Match(g, ""); err != nil {
			add("match.alertNames[%d]: invalid glob %q: %v", i, g, err)
		}
	}

	if err := checkRegex(s.Match.TextPattern); err != nil {
		add("match.textPattern: %v", err)
	}

	if s.Budgets.MaxTokens < 0 {
		add("budgets.maxTokens: must not be negative")
	}

	if !(s.Budgets.MaxCostUSD >= 0) || math.IsInf(s.Budgets.MaxCostUSD, 1) {
		add("budgets.maxCostUSD: must be a dollar amount that is not negative")
	}

	if s.Budgets.MaxToolCalls < 0 {
		add("budgets.maxToolCalls: must not be negative")
	}

	if s.Budgets.MaxWallTime < 0 {
		add("budgets.maxWallTime: must not be negative")
	}

	if s.WatcherDefaults != nil {
		problems = append(problems, s.WatcherDefaults.validate("watcherDefaults")...)
	}

	seen := map[string]bool{}

	for i, w := range s.Watchers {
		at := fmt.Sprintf("watchers[%d]", i)

		switch {
		case w.Name == "":
			add("%s.name: required", at)
		case !namePattern.MatchString(w.Name):
			add("%s.name: %q %s", at, w.Name, nameRule)
		case seen[w.Name]:
			add("%s.name: watcher %q is defined more than once", at, w.Name)
		}

		seen[w.Name] = true

		problems = append(problems, w.validate(at)...)
	}

	return problems
}

// validate checks the shape of one watcher.
func (w *Watcher) validate(at string) []string {
	var problems []string

	for i, g := range w.Match.AlertName {
		if _, err := path.Match(g, ""); err != nil {
			problems = append(problems, fmt.Sprintf("%s.match.alertName[%d]: invalid glob %q: %v", at, i, g, err))
		}
	}

	if err := checkRegex(w.Match.Text); err != nil {
		problems = append(problems, fmt.Sprintf("%s.match.text: %v", at, err))
	}

	if !slices.Contains(knownCheckTypes, w.Check.Type) {
		problems = append(problems, fmt.Sprintf("%s.check.type: expected one of %v, got %q", at, knownCheckTypes, w.Check.Type))
	}

	if w.Check.Expect != nil {
		if err := checkRegex(w.Check.Expect.BodyRegex); err != nil {
			problems = append(problems, fmt.Sprintf("%s.check.expect.bodyRegex: %v", at, err))
		}
	}

	if w.Check.Condition != nil {
		if err := checkRegex(w.Check.Condition.TextRegex); err != nil {
			problems = append(problems, fmt.Sprintf("%s.check.condition.textRegex: %v", at, err))
		}
	}

	if w.Check.Timeout < 0 {
		problems = append(problems, at+".check.timeout: must not be negative")
	}

	return append(problems, w.WatcherSettings.validate(at)...)
}

// validate checks that no timing setting is negative.
func (ws *WatcherSettings) validate(at string) []string {
	var problems []string

	for key, d := range map[string]time.Duration{
		"interval":     ws.Interval,
		"timeout":      ws.Timeout,
		"checkTimeout": ws.CheckTimeout,
	} {
		if d < 0 {
			problems = append(problems, fmt.Sprintf("%s.%s: must not be negative", at, key))
		}
	}

	if ws.PassesToRecover < 0 {
		problems = append(problems, at+".passesToRecover: must not be negative")
	}

	slices.Sort(problems)

	return problems
}

// checkRegex reports whether a non-empty pattern compiles.
func checkRegex(pattern string) error {
	if pattern == "" {
		return nil
	}

	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("invalid regular expression: %w", err)
	}

	return nil
}

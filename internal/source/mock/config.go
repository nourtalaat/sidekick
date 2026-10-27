package mock

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Default is what a call that no fixture matches returns.
type Default string

// The defaults.
const (
	// DefaultNoData returns a normal result with NoData set and NoDataText.
	DefaultNoData Default = "no_data"
	// DefaultError returns a tool error.
	DefaultError Default = "error"
)

// NoDataText is the text of a result with no data that sets no text of
// its own.
const NoDataText = "No data for this query"

// knownEvidenceKinds are the evidence kinds a response may use.
var knownEvidenceKinds = []source.EvidenceKind{
	source.EvidenceLogQuery, source.EvidenceMetric, source.EvidenceTrace, source.EvidenceCommit,
	source.EvidenceDeploy, source.EvidenceTicket, source.EvidenceResource, source.EvidenceIncident,
	source.EvidenceMessage, source.EvidenceDashboard, source.EvidenceOther,
}

// Config is one mock source: a scenario's entry under sources.
type Config struct {
	// Tools names the snapshot file the tool list comes from.
	Tools Tools `yaml:"tools"`
	// Fixtures answer the calls, tried in order.
	Fixtures []Fixture `yaml:"fixtures"`
	// Unavailable makes the source fail to list its tools and every call,
	// as a source that is down does.
	Unavailable bool `yaml:"unavailable"`
	// Default is what a call no fixture matches returns. Empty means
	// DefaultNoData.
	Default Default `yaml:"default"`
	// Now is the clock that within matchers count from: the scenario's
	// clock. Zero means the current time.
	Now time.Time `yaml:"-"`
}

// Tools says where the tool list comes from.
type Tools struct {
	// Snapshot is a JSON file of the form {"tools": [{"name",
	// "description", "inputSchema"}]}, as the files in bench/tools are.
	Snapshot string `yaml:"snapshot"`
}

// Fixture answers the calls to the tools that match Tool and whose
// arguments match Match.
type Fixture struct {
	// Tool is a tool name, or a glob in path.Match syntax.
	Tool string `yaml:"tool"`
	// Match holds the matchers of the call's arguments.
	Match Match `yaml:"match"`
	// Strict makes a call that has an argument Match does not name fail to
	// match. Without it such arguments are ignored.
	Strict bool `yaml:"strict"`
	// Respond is the one answer. Set it or Responses, not both.
	Respond *Response `yaml:"respond"`
	// Responses are answers given in order, one per matching call; the
	// last one repeats.
	Responses []Response `yaml:"responses"`
}

// Match holds the matchers of one fixture.
type Match struct {
	// Args are matchers by argument name. Every one must hold.
	Args map[string]Matcher `yaml:"args"`
}

// Matcher checks one argument. Every operator that is set must hold, and at
// least one must be set.
type Matcher struct {
	// Equals holds when the argument is this value. Numbers compare by
	// value, so 5 equals 5.0.
	Equals any `yaml:"equals"`
	// Regex holds when the argument is a string the expression matches
	// somewhere. Use ^ and $ to match all of it, and (?i) to ignore case.
	Regex string `yaml:"regex"`
	// Contains holds when the argument is a string with this text in it,
	// or a list with this value in it.
	Contains any `yaml:"contains"`
	// OneOf holds when the argument equals one of the values.
	OneOf []any `yaml:"oneOf"`
	// Exists holds, when true, if the call has the argument, and, when
	// false, if it does not.
	Exists *bool `yaml:"exists"`
	// GTE and LTE hold when the argument is a number at least or at most
	// this value.
	GTE *float64 `yaml:"gte"`
	LTE *float64 `yaml:"lte"`
	// Within holds when the argument is an RFC 3339 time no further than
	// this from the scenario clock, before or after it.
	Within time.Duration `yaml:"within"`
}

// Response is one answer to a call.
type Response struct {
	// Text is the result the model reads.
	Text string `yaml:"text"`
	// NoData marks a result that found nothing. Without Text it reads
	// NoDataText.
	NoData bool `yaml:"noData"`
	// Evidence are the links the result gives.
	Evidence []Evidence `yaml:"evidence"`
}

// Evidence is one link of a response.
type Evidence struct {
	Kind  source.EvidenceKind `yaml:"kind"`
	Title string              `yaml:"title"`
	URL   string              `yaml:"url"`
}

// Validate checks the config without reading the snapshot. Each error
// starts with the key path of its field.
func (c Config) Validate() error {
	var errs []error

	if !c.Unavailable && c.Tools.Snapshot == "" {
		errs = append(errs, errors.New("tools.snapshot: required unless unavailable is true"))
	}

	if !slices.Contains([]Default{"", DefaultNoData, DefaultError}, c.Default) {
		errs = append(errs, fmt.Errorf("default: must be %s or %s, not %q", DefaultNoData, DefaultError, c.Default))
	}

	for i, f := range c.Fixtures {
		if err := f.validate(); err != nil {
			errs = append(errs, prefixLines(fmt.Sprintf("fixtures[%d].", i), err))
		}
	}

	return errors.Join(errs...)
}

// validate checks one fixture. Each error starts with a key path relative
// to the fixture.
func (f Fixture) validate() error {
	var errs []error

	if f.Tool == "" {
		errs = append(errs, errors.New("tool: required"))
	} else if _, err := path.Match(f.Tool, ""); err != nil {
		errs = append(errs, fmt.Errorf("tool: invalid glob %q: %w", f.Tool, err))
	}

	for _, name := range slices.Sorted(maps.Keys(f.Match.Args)) {
		if err := f.Match.Args[name].validate(); err != nil {
			errs = append(errs, prefixLines("match.args."+name+": ", err))
		}
	}

	switch {
	case f.Respond == nil && len(f.Responses) == 0:
		errs = append(errs, errors.New("respond: set respond or responses"))
	case f.Respond != nil && len(f.Responses) > 0:
		errs = append(errs, errors.New("respond: set respond or responses, not both"))
	case f.Respond != nil:
		if err := f.Respond.validate(); err != nil {
			errs = append(errs, prefixLines("respond.", err))
		}
	}

	for i, r := range f.Responses {
		if err := r.validate(); err != nil {
			errs = append(errs, prefixLines(fmt.Sprintf("responses[%d].", i), err))
		}
	}

	return errors.Join(errs...)
}

// validate checks that at least one operator is set and that each one can
// be used.
func (m Matcher) validate() error {
	if m.Equals == nil && m.Regex == "" && m.Contains == nil && m.OneOf == nil &&
		m.Exists == nil && m.GTE == nil && m.LTE == nil && m.Within == 0 {
		return errors.New("set at least one of equals, regex, contains, oneOf, exists, gte, lte and within")
	}

	var errs []error

	if m.Regex != "" {
		if _, err := regexp.Compile(m.Regex); err != nil {
			errs = append(errs, fmt.Errorf("regex: %w", err))
		}
	}

	if m.OneOf != nil && len(m.OneOf) == 0 {
		errs = append(errs, errors.New("oneOf: must not be empty"))
	}

	if m.Within < 0 {
		errs = append(errs, errors.New("within: must not be negative"))
	}

	if m.GTE != nil && m.LTE != nil && *m.GTE > *m.LTE {
		errs = append(errs, errors.New("gte: must not be more than lte"))
	}

	return errors.Join(errs...)
}

// validate checks the evidence of a response. Each error starts with a key
// path relative to the response.
func (r Response) validate() error {
	var errs []error

	for i, e := range r.Evidence {
		if !slices.Contains(knownEvidenceKinds, e.Kind) {
			errs = append(errs, fmt.Errorf("evidence[%d].kind: unknown kind %q", i, e.Kind))
		}

		if e.Title == "" {
			errs = append(errs, fmt.Errorf("evidence[%d].title: required", i))
		}
	}

	return errors.Join(errs...)
}

// prefixLines puts prefix before each line of err, so every problem of a
// joined error keeps its full key path.
func prefixLines(prefix string, err error) error {
	var errs []error

	for line := range strings.Lines(err.Error()) {
		errs = append(errs, errors.New(prefix+strings.TrimSuffix(line, "\n")))
	}

	return errors.Join(errs...)
}

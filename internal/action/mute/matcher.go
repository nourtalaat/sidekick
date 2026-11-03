package mute

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
)

// The limits on the matchers of one mute. They keep a proposal short enough
// for an approver to read whole.
const (
	// MaxMatchers is the most matchers one mute may have.
	MaxMatchers = 10
	// MaxMatcherNameBytes is the longest label name of a matcher.
	MaxMatcherNameBytes = 64
	// MaxMatcherValueBytes is the longest value or pattern of a matcher.
	MaxMatcherValueBytes = 200
)

// labelName is the shape of a label name that every Alertmanager accepts,
// whatever its UTF-8 setting.
var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

var (
	errEmptyName  = errors.New("the label name is empty")
	errEmptyValue = errors.New("the value is empty")
	errBadValue   = errors.New("the value is not valid UTF-8 text")
)

// valueEscaper escapes a value for a matcher written as text.
var valueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// Matcher is a label matcher as the Alertmanager v2 API writes it. Both
// flags are always written out: IsEqual false means "not equal".
type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

// CheckMatchers checks one mute's matchers: from one to MaxMatchers of
// them, each with a label name of letters, digits and underscores, a
// value, both within their byte limits and not empty, and a pattern that
// compiles when IsRegex is set. The error wraps action.ErrLimit.
func CheckMatchers(matchers []Matcher) error {
	if len(matchers) == 0 || len(matchers) > MaxMatchers {
		return fmt.Errorf("a mute needs from 1 to %d matchers, and this one has %d: %w", MaxMatchers, len(matchers), action.ErrLimit)
	}

	for i, m := range matchers {
		if err := m.check(); err != nil {
			return fmt.Errorf("matcher %d: %w: %w", i+1, err, action.ErrLimit)
		}
	}

	return nil
}

// SameMatchers reports whether a and b hold the same matchers, in any
// order.
func SameMatchers(a, b []Matcher) bool {
	if len(a) != len(b) {
		return false
	}

	return slices.Equal(sorted(a), sorted(b))
}

// Expression writes the matcher as an Alertmanager filter, such as
// service="checkout" or team=~"pay.*": the operator is =, !=, =~ or !~,
// and the value is in double quotes with backslashes, quotes and line
// breaks escaped.
func (m Matcher) Expression() string {
	return m.Name + m.operator() + `"` + valueEscaper.Replace(m.Value) + `"`
}

// check checks one matcher.
func (m Matcher) check() error {
	switch {
	case m.Name == "":
		return errEmptyName
	case m.Value == "":
		return errEmptyValue
	case len(m.Name) > MaxMatcherNameBytes:
		return fmt.Errorf("the label name is longer than %d bytes", MaxMatcherNameBytes)
	case len(m.Value) > MaxMatcherValueBytes:
		return fmt.Errorf("the value is longer than %d bytes", MaxMatcherValueBytes)
	case !labelName.MatchString(m.Name):
		return fmt.Errorf("%q is not a label name: use letters, digits and underscores, and do not start with a digit", m.Name)
	case !utf8.ValidString(m.Value):
		return errBadValue
	}

	if m.IsRegex {
		if _, err := regexp.Compile(m.Value); err != nil {
			return fmt.Errorf("the pattern %q is not a regular expression: %w", m.Value, err)
		}
	}

	return nil
}

// operator is the matcher's operator in an Alertmanager filter.
func (m Matcher) operator() string {
	switch {
	case m.IsRegex && m.IsEqual:
		return "=~"
	case m.IsRegex:
		return "!~"
	case m.IsEqual:
		return "="
	default:
		return "!="
	}
}

// sorted returns a copy of matchers in a fixed order.
func sorted(matchers []Matcher) []Matcher {
	out := slices.Clone(matchers)

	slices.SortFunc(out, func(a, b Matcher) int {
		return cmp.Or(
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.Value, b.Value),
			compareBool(a.IsRegex, b.IsRegex),
			compareBool(a.IsEqual, b.IsEqual),
		)
	})

	return out
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	default:
		return 1
	}
}

package mute

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nourtalaat/sidekick/internal/action"
)

// Scope is a set of labels, each with the values it allows, as globs where
// * matches any text and every other character stands for itself. A mute
// is inside a scope when it pins every label of the scope.
type Scope struct {
	Labels map[string][]string `yaml:"labels" json:"labels"`
}

// CheckScopes checks the scopes of a config, without a network call: at
// least one scope, and in each at least one label with at least one value,
// all not empty. A scope with no labels would allow muting everything. Each
// error starts with the key of its field, such as
// limits.scopes[0].labels.service.
func CheckScopes(scopes []Scope) error {
	if len(scopes) == 0 {
		return errors.New("limits.scopes: at least one scope is required")
	}

	var errs []error

	for i, s := range scopes {
		key := fmt.Sprintf("limits.scopes[%d].labels", i)

		if len(s.Labels) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one label is required, because a scope with no labels would allow muting everything", key))

			continue
		}

		for _, name := range slices.Sorted(maps.Keys(s.Labels)) {
			errs = append(errs, checkLabel(key+"."+name, name, s.Labels[name])...)
		}
	}

	return errors.Join(errs...)
}

// Permits applies the scope rule: the matchers are allowed when, for at
// least one scope, every label of the scope is pinned by an equality
// matcher (isEqual true and isRegex false) whose value matches one of that
// label's globs. Further matchers of any kind are allowed, because each can
// only narrow what is muted. The error says which labels each scope needs,
// and wraps action.ErrLimit.
func Permits(scopes []Scope, matchers []Matcher) error {
	for _, s := range scopes {
		if s.pinnedBy(matchers) {
			return nil
		}
	}

	return fmt.Errorf("the matchers do not pin every label of any scope with an equality matcher (=) on an allowed value; %s: %w",
		DescribeScopes(scopes), action.ErrLimit)
}

// pinnedBy reports whether the matchers pin every label of the scope.
func (s Scope) pinnedBy(matchers []Matcher) bool {
	if len(s.Labels) == 0 {
		return false
	}

	for name, globs := range s.Labels {
		pinned := slices.ContainsFunc(matchers, func(m Matcher) bool {
			return m.Name == name && m.IsEqual && !m.IsRegex && slices.ContainsFunc(globs, func(g string) bool { return matchGlob(g, m.Value) })
		})
		if !pinned {
			return false
		}
	}

	return true
}

// DescribeScopes lists the scopes in words, for an error or for the
// description the model reads: "scope 1 needs alertname in [A, B] and
// service in [checkout-*]; scope 2 needs ...".
func DescribeScopes(scopes []Scope) string {
	parts := make([]string, 0, len(scopes))

	for i, s := range scopes {
		labels := make([]string, 0, len(s.Labels))

		for _, name := range slices.Sorted(maps.Keys(s.Labels)) {
			labels = append(labels, name+" in ["+strings.Join(s.Labels[name], ", ")+"]")
		}

		parts = append(parts, fmt.Sprintf("scope %d needs %s", i+1, strings.Join(labels, " and ")))
	}

	return strings.Join(parts, "; ")
}

// checkLabel checks the name and the globs of one label of a scope.
func checkLabel(key, name string, globs []string) []error {
	var errs []error

	if !labelName.MatchString(name) {
		errs = append(errs, fmt.Errorf("%s: %q is not a label name: use letters, digits and underscores, and do not start with a digit", key, name))
	}

	if len(globs) == 0 {
		errs = append(errs, fmt.Errorf("%s: at least one value is required", key))
	}

	for _, g := range globs {
		switch {
		case g == "":
			errs = append(errs, fmt.Errorf("%s: a value must not be empty", key))
		case !utf8.ValidString(g):
			errs = append(errs, fmt.Errorf("%s: %q is not valid UTF-8 text", key, g))
		}
	}

	return errs
}

// matchGlob reports whether value matches pattern, where * matches any
// text, including none, and every other character matches itself.
func matchGlob(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}

	first, last := parts[0], parts[len(parts)-1]
	if !strings.HasPrefix(value, first) {
		return false
	}

	rest := value[len(first):]

	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, mid)
		if i < 0 {
			return false
		}

		rest = rest[i+len(mid):]
	}

	return strings.HasSuffix(rest, last)
}

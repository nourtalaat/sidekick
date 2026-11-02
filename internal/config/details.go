package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The limits of a route's details. File routes, runtime routes, the API and
// benchmark scenarios all check them with CheckRouteDetails.
const (
	// MaxRouteDetails is the most keys one route may have in details.
	MaxRouteDetails = 20
	// MaxDetailKeyLength is the most characters in a key of details.
	MaxDetailKeyLength = 64
	// MaxDetailValueLength is the most characters in a value of details.
	MaxDetailValueLength = 500
)

// detailKeyPattern is the rule for the keys of a route's details.
var detailKeyPattern = regexp.MustCompile(fmt.Sprintf(`^[A-Za-z0-9_.-]{1,%d}$`, MaxDetailKeyLength))

// CheckRouteDetails returns the problems of a route's details, one message
// each, with the keys in sorted order. The details are shown to the model
// as trusted text, so a value must be one line of printable characters: it
// may hold no control character, which includes line breaks and tabs.
func CheckRouteDetails(details map[string]string) []string {
	var problems []string

	if len(details) > MaxRouteDetails {
		problems = append(problems, fmt.Sprintf("at most %d keys are allowed, got %d", MaxRouteDetails, len(details)))
	}

	for _, key := range slices.Sorted(maps.Keys(details)) {
		if !detailKeyPattern.MatchString(key) {
			problems = append(problems, fmt.Sprintf("key %q must be 1 to %d characters from letters, digits, \"-\", \"_\" and \".\"", key, MaxDetailKeyLength))
		}

		if msg := detailValueProblem(details[key]); msg != "" {
			problems = append(problems, fmt.Sprintf("key %q: %s", key, msg))
		}
	}

	return problems
}

// detailValueProblem says what is wrong with a value of details, or returns
// "" when it is fine.
func detailValueProblem(value string) string {
	switch {
	case strings.TrimSpace(value) == "":
		return "the value must not be empty"
	case !utf8.ValidString(value):
		return "the value must be valid UTF-8 text"
	case strings.IndexFunc(value, unicode.IsControl) >= 0:
		return "the value must be one line with no control characters"
	case utf8.RuneCountInString(value) > MaxDetailValueLength:
		return fmt.Sprintf("the value must be at most %d characters, got %d", MaxDetailValueLength, utf8.RuneCountInString(value))
	}

	return ""
}

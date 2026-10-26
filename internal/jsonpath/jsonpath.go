// Package jsonpath finds values in decoded JSON with a small subset of
// JSONPath: the root $, .field, ["field"], [n] and [*]. Sidekick implements
// the subset itself so its behaviour is fully tested and needs no
// dependency. Webhook adapters use it for their field paths, and checks
// that read tool results can use it too.
package jsonpath

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrSyntax is returned, wrapped, for a path that does not parse.
var ErrSyntax = errors.New("syntax error")

// stepKind says what one step of a path selects.
type stepKind int

const (
	stepField stepKind = iota
	stepIndex
	stepAll
)

// step is one selector of a path, such as .name or [2].
type step struct {
	kind  stepKind
	field string
	index int
}

// Path is a parsed JSON path. The zero value is no path at all: IsZero
// reports it, and Find finds nothing with it.
type Path struct {
	text  string
	steps []step
}

// Parse parses a path such as $.alerts[0].labels["app.kubernetes.io/name"].
// A path starts with $ and goes on with any number of steps:
//
//   - .field, where field starts with a letter or _ and holds letters,
//     digits, _ and -;
//   - ["field"], a field name as a JSON string, for any other name;
//   - [n], the element at index n of an array, counting from 0;
//   - [*], every element of an array.
func Parse(s string) (Path, error) {
	rest, ok := strings.CutPrefix(s, "$")
	if !ok {
		return Path{}, fmt.Errorf("%w in %q: must start with $", ErrSyntax, s)
	}

	p := Path{text: s}

	for rest != "" {
		st, next, err := parseStep(rest)
		if err != nil {
			return Path{}, fmt.Errorf("%w in %q at offset %d: %w", ErrSyntax, s, len(s)-len(rest), err)
		}

		p.steps = append(p.steps, st)
		rest = next
	}

	return p, nil
}

// String returns the path as it was written.
func (p Path) String() string {
	return p.text
}

// IsZero reports whether p is the zero Path, which config uses for a path
// that is not set.
func (p Path) IsZero() bool {
	return p.text == ""
}

// UnmarshalText parses text into p, so a config field can hold a Path.
func (p *Path) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return fmt.Errorf("parse JSON path: %w", err)
	}

	*p = parsed

	return nil
}

// Find returns the values the path selects in doc, a value decoded by
// encoding/json into any (objects as map[string]any, arrays as []any). A
// step that does not apply, such as a field of an array or an index past
// the end, selects nothing, so a path that finds nothing returns an empty
// slice. A field that is present with the value null is found, as nil.
// [*] returns the elements in array order.
func (p Path) Find(doc any) []any {
	if p.IsZero() {
		return nil
	}

	current := []any{doc}

	for _, st := range p.steps {
		var next []any

		for _, v := range current {
			next = st.appendSelected(next, v)
		}

		if len(next) == 0 {
			return nil
		}

		current = next
	}

	return current
}

// appendSelected adds what the step selects in v to out.
func (st step) appendSelected(out []any, v any) []any {
	switch st.kind {
	case stepField:
		if obj, ok := v.(map[string]any); ok {
			if value, ok := obj[st.field]; ok {
				out = append(out, value)
			}
		}
	case stepIndex:
		if arr, ok := v.([]any); ok && st.index < len(arr) {
			out = append(out, arr[st.index])
		}
	case stepAll:
		if arr, ok := v.([]any); ok {
			out = append(out, arr...)
		}
	}

	return out
}

// parseStep parses the step at the start of s and returns the rest of s.
func parseStep(s string) (step, string, error) {
	switch s[0] {
	case '.':
		return parseDotField(s[1:])
	case '[':
		return parseBracket(s[1:])
	default:
		return step{}, "", fmt.Errorf("expected . or [, got %q", s[0])
	}
}

// parseDotField parses the field name after a dot.
func parseDotField(s string) (step, string, error) {
	n := 0
	for n < len(s) && isFieldByte(s[n], n == 0) {
		n++
	}

	if n == 0 {
		return step{}, "", errors.New(`expected a field name after "."; use ["name"] for names with other characters`)
	}

	return step{kind: stepField, field: s[:n]}, s[n:], nil
}

// parseBracket parses what follows a [, up to and including the ].
func parseBracket(s string) (step, string, error) {
	switch {
	case strings.HasPrefix(s, "*]"):
		return step{kind: stepAll}, s[len("*]"):], nil
	case strings.HasPrefix(s, `"`):
		return parseQuotedField(s)
	default:
		return parseIndex(s)
	}
}

// parseQuotedField parses a JSON string and the ] after it.
func parseQuotedField(s string) (step, string, error) {
	end := closingQuote(s)
	if end < 0 {
		return step{}, "", errors.New("unterminated field name")
	}

	var field string
	if err := json.Unmarshal([]byte(s[:end+1]), &field); err != nil {
		return step{}, "", fmt.Errorf("field name %s: %w", s[:end+1], err)
	}

	rest, ok := strings.CutPrefix(s[end+1:], "]")
	if !ok {
		return step{}, "", errors.New(`expected "]" after the field name`)
	}

	return step{kind: stepField, field: field}, rest, nil
}

// parseIndex parses an array index and the ] after it.
func parseIndex(s string) (step, string, error) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}

	if n == 0 {
		return step{}, "", errors.New(`expected a field name in quotes, an index or "*" after "["`)
	}

	rest, ok := strings.CutPrefix(s[n:], "]")
	if !ok {
		return step{}, "", errors.New(`expected "]" after the index`)
	}

	index, err := strconv.Atoi(s[:n])
	if err != nil {
		return step{}, "", fmt.Errorf("index %s: %w", s[:n], err)
	}

	return step{kind: stepIndex, index: index}, rest, nil
}

// closingQuote returns the index of the quote that ends the JSON string at
// the start of s, or -1 when it does not end.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}

	return -1
}

// isFieldByte reports whether c can be part of a field name after a dot.
// The first byte cannot be a digit or -.
func isFieldByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case c >= '0' && c <= '9', c == '-':
		return !first
	default:
		return false
	}
}

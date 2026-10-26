package config

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Error is one problem in a config file. Path names the field, for example
// routes[0].outputs[1]; it is empty for problems with the file as a whole.
type Error struct {
	Path    string
	Message string
}

// String returns the error as "path: message".
func (e Error) String() string {
	if e.Path == "" {
		return e.Message
	}

	return e.Path + ": " + e.Message
}

// Errors lists every problem found in a config file, sorted by path. Load,
// Validate and Build return it, so a caller can print one problem per line.
type Errors []Error

// Error implements error with one problem per line.
func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, item := range e {
		lines[i] = item.String()
	}

	return strings.Join(lines, "\n")
}

// sorted returns the errors sorted by path, comparing list indexes as
// numbers so routes[2] comes before routes[10], then by message. It drops
// exact duplicates.
func (e Errors) sorted() Errors {
	out := slices.Clone(e)
	slices.SortStableFunc(out, func(a, b Error) int {
		if c := comparePaths(a.Path, b.Path); c != 0 {
			return c
		}

		return strings.Compare(a.Message, b.Message)
	})

	return slices.Compact(out)
}

// errorList collects errors while a config is decoded and checked.
type errorList struct {
	items Errors
}

// add records a problem at path.
func (l *errorList) add(path, format string, args ...any) {
	l.items = append(l.items, Error{Path: path, Message: fmt.Sprintf(format, args...)})
}

// addAll records problems found by another step.
func (l *errorList) addAll(items Errors) {
	l.items = append(l.items, items...)
}

// err returns the sorted problems, or nil when there are none.
func (l *errorList) err() error {
	if len(l.items) == 0 {
		return nil
	}

	return l.items.sorted()
}

// comparePaths orders two field paths, comparing runs of digits by their
// numeric value and everything else byte by byte.
func comparePaths(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, restA := cutDigits(a)
			nb, restB := cutDigits(b)

			if c := cmp.Compare(len(na), len(nb)); c != 0 {
				return c
			}

			if c := strings.Compare(na, nb); c != 0 {
				return c
			}

			a, b = restA, restB

			continue
		}

		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}

		a, b = a[1:], b[1:]
	}

	return cmp.Compare(len(a), len(b))
}

// cutDigits splits s after its leading digits, without leading zeros.
func cutDigits(s string) (digits, rest string) {
	end := 0
	for end < len(s) && isDigit(s[end]) {
		end++
	}

	digits = strings.TrimLeft(s[:end], "0")

	return digits, s[end:]
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// Limits on a difference, so an approver can read all of it.
const (
	// MaxChanges is the most fields one change may differ in.
	MaxChanges = 100
	// maxDiffDepth is how deeply nested a definition may be.
	maxDiffDepth = 32
)

// ErrTooManyChanges is wrapped, with ErrLimit, when two definitions differ
// in more than MaxChanges fields or nest deeper than a person can review.
var ErrTooManyChanges = errors.New("too large a difference to review")

// Delta is one field that differs between two versions of a definition. A
// nil Before means the field did not exist, a nil After that it is removed.
// The empty Path stands for the whole definition, which is created or
// removed.
type Delta struct {
	Path   Path
	Before json.RawMessage
	After  json.RawMessage
}

// Change is a Delta written for a person to read: the path, and the values
// as text in which every character that could hide something is a \u
// escape, as DisplayParams writes them. A nil Before or After means the
// field is absent on that side. The values come from the monitoring tool
// and from the model, so a screen shows them as plain text.
type Change struct {
	Path   string  `json:"path"`
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
}

// Changes returns the field-by-field difference between the snapshot's
// Before and After, sorted by path.
func Changes(s Snapshot) ([]Change, error) {
	deltas, err := Diff(s.Before, s.After)
	if err != nil {
		return nil, fmt.Errorf("compare the snapshot: %w", err)
	}

	return Display(deltas), nil
}

// Display writes deltas for a person to read.
func Display(deltas []Delta) []Change {
	out := make([]Change, 0, len(deltas))

	for _, d := range deltas {
		out = append(out, Change{Path: spell(d.Path.String()), Before: displayValue(d.Before), After: displayValue(d.After)})
	}

	return out
}

// Deletes reports whether the snapshot removes the whole definition.
func (s Snapshot) Deletes() bool {
	return isAbsent(s.After) && !isAbsent(s.Before)
}

// Diff returns the fields that differ between two versions of a definition,
// sorted by path. Objects are compared key by key. Arrays are compared
// whole, except that two arrays of the same length whose elements are all
// objects are compared position by position, as a list of rules is. A
// value of another type, or a number written another way, counts as
// different only when it is not equal. An absent or null before or after
// stands for a definition that does not exist.
func Diff(before, after json.RawMessage) ([]Delta, error) {
	a, aok, err := decodeSide(before)
	if err != nil {
		return nil, fmt.Errorf("decode the before version: %w", err)
	}

	b, bok, err := decodeSide(after)
	if err != nil {
		return nil, fmt.Errorf("decode the after version: %w", err)
	}

	w := &walker{}

	switch {
	case !aok && !bok:
		return nil, nil
	case !aok || !bok:
		if err := w.leaf(nil, a, aok, b, bok); err != nil {
			return nil, err
		}
	default:
		if err := w.walk(nil, a, b, 0); err != nil {
			return nil, err
		}
	}

	return w.deltas, nil
}

// decodeSide reads one version, and reports whether it exists.
func decodeSide(raw json.RawMessage) (any, bool, error) {
	if isAbsent(raw) {
		return nil, false, nil
	}

	v, err := decode(raw)

	return v, true, err
}

// isAbsent reports whether raw says nothing, or JSON null.
func isAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)

	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// walker collects the deltas of one comparison.
type walker struct {
	deltas []Delta
}

// walk compares two values that exist at path.
func (w *walker) walk(path Path, a, b any, depth int) error {
	if depth > maxDiffDepth {
		return fmt.Errorf("nested more than %d levels deep: %w: %w", maxDiffDepth, ErrTooManyChanges, ErrLimit)
	}

	switch av := a.(type) {
	case map[string]any:
		if bv, ok := b.(map[string]any); ok {
			return w.walkObjects(path, av, bv, depth)
		}
	case []any:
		if bv, ok := b.([]any); ok && objectPairs(av, bv) {
			for i := range av {
				if err := w.walk(append(slices.Clone(path), "["+strconv.Itoa(i)+"]"), av[i], bv[i], depth+1); err != nil {
					return err
				}
			}

			return nil
		}
	}

	if equal(a, b) {
		return nil
	}

	return w.leaf(path, a, true, b, true)
}

// walkObjects compares two objects key by key, in key order.
func (w *walker) walkObjects(path Path, a, b map[string]any, depth int) error {
	keys := slices.Sorted(maps.Keys(a))
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}

	slices.Sort(keys)

	for _, k := range slices.Compact(keys) {
		av, aok := a[k]
		bv, bok := b[k]
		child := append(slices.Clone(path), k)

		if aok && bok {
			if err := w.walk(child, av, bv, depth+1); err != nil {
				return err
			}

			continue
		}

		if err := w.leaf(child, av, aok, bv, bok); err != nil {
			return err
		}
	}

	return nil
}

// leaf records a difference at path. aok and bok say whether each side has
// the field.
func (w *walker) leaf(path Path, a any, aok bool, b any, bok bool) error {
	if len(w.deltas) >= MaxChanges {
		return fmt.Errorf("more than %d fields differ: %w: %w", MaxChanges, ErrTooManyChanges, ErrLimit)
	}

	d := Delta{Path: path}

	var err error

	if aok {
		if d.Before, err = encode(a, ""); err != nil {
			return fmt.Errorf("encode the before value: %w", err)
		}
	}

	if bok {
		if d.After, err = encode(b, ""); err != nil {
			return fmt.Errorf("encode the after value: %w", err)
		}
	}

	w.deltas = append(w.deltas, d)

	return nil
}

// objectPairs reports whether a and b have the same length, more than zero,
// and every element of both is an object.
func objectPairs(a, b []any) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}

	for i := range a {
		_, aok := a[i].(map[string]any)
		_, bok := b[i].(map[string]any)

		if !aok || !bok {
			return false
		}
	}

	return true
}

// equal reports whether two decoded values are the same JSON value. Numbers
// are compared by value.
func equal(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}

		for k, x := range av {
			y, ok := bv[k]
			if !ok || !equal(x, y) {
				return false
			}
		}

		return true
	case []any:
		bv, ok := b.([]any)

		return ok && slices.EqualFunc(av, bv, equal)
	case json.Number:
		bv, ok := b.(json.Number)

		return ok && sameNumber(av, bv)
	default:
		return a == b
	}
}

// sameNumber compares two numbers: as whole numbers when both are, so large
// ones lose nothing, and as floating point numbers otherwise.
func sameNumber(a, b json.Number) bool {
	if x, err := strconv.ParseInt(string(a), 10, 64); err == nil {
		if y, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			return x == y
		}
	}

	x, aerr := strconv.ParseFloat(string(a), 64)
	y, berr := strconv.ParseFloat(string(b), 64)

	if aerr != nil || berr != nil {
		return a == b
	}

	return x == y
}

// displayValue is the text of a value for a person, or nil for an absent one.
func displayValue(raw json.RawMessage) *string {
	if raw == nil {
		return nil
	}

	text := spell(string(raw))

	return &text
}

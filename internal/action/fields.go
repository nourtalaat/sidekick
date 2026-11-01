package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// anyIndex is the path segment that matches every position of an array in
// a list of allowed fields.
const anyIndex = "[*]"

// ErrPath is wrapped by every problem with a path or with an edit that
// uses one.
var ErrPath = errors.New("invalid field path")

// Path names one field of a JSON definition, for example
// options.thresholds.critical or data[0].model.expr. Its segments are the
// keys of objects, and a position in an array written [n]. In a list of
// allowed fields, [*] stands for any position.
type Path []string

// ParsePath parses a path written as keys joined by dots, each key followed
// by any number of [n] or [*] positions. A key is any text without a dot or
// a bracket, and is not empty.
func ParsePath(s string) (Path, error) {
	if s == "" {
		return nil, fmt.Errorf("%w: the path is empty", ErrPath)
	}

	var p Path

	for key := range strings.SplitSeq(s, ".") {
		name, positions, hasPositions := strings.Cut(key, "[")
		if name == "" || strings.Contains(name, "]") {
			return nil, fmt.Errorf("%w: %q has an empty or malformed key", ErrPath, s)
		}

		p = append(p, name)

		if !hasPositions {
			continue
		}

		for rest := "[" + positions; rest != ""; {
			end := strings.Index(rest, "]")
			if !strings.HasPrefix(rest, "[") || end < 0 || !validPosition(rest[:end+1]) {
				return nil, fmt.Errorf("%w: %q has a malformed position", ErrPath, s)
			}

			p = append(p, rest[:end+1])
			rest = rest[end+1:]
		}
	}

	return p, nil
}

// validPosition reports whether seg is [n] or [*].
func validPosition(seg string) bool {
	inner, ok := strings.CutSuffix(strings.TrimPrefix(seg, "["), "]")
	if !ok {
		return false
	}

	if inner == "*" {
		return true
	}

	n, err := strconv.Atoi(inner)

	return err == nil && n >= 0 && strconv.Itoa(n) == inner
}

// String writes the path as ParsePath reads it.
func (p Path) String() string {
	var b strings.Builder

	for i, seg := range p {
		if i > 0 && !strings.HasPrefix(seg, "[") {
			b.WriteByte('.')
		}

		b.WriteString(seg)
	}

	return b.String()
}

// Within reports whether p is allowed by the path allowed: allowed is p, or
// a path that p goes on from at a segment boundary. So options.thresholds
// allows options.thresholds.critical, but options.thresholds.critical does
// not allow options.thresholds.
func (p Path) Within(allowed Path) bool {
	if len(allowed) == 0 || len(allowed) > len(p) {
		return false
	}

	for i, seg := range allowed {
		if seg == p[i] || (seg == anyIndex && strings.HasPrefix(p[i], "[")) {
			continue
		}

		return false
	}

	return true
}

// ParsePaths parses every path of a list of allowed fields.
func ParsePaths(list []string) ([]Path, error) {
	out := make([]Path, 0, len(list))

	for _, s := range list {
		p, err := ParsePath(s)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", s, err)
		}

		out = append(out, p)
	}

	return out, nil
}

// CheckFields returns an error wrapping ErrLimit for the first change whose
// path is not within one of the allowed fields.
func CheckFields(allowed []Path, deltas []Delta) error {
	for _, d := range deltas {
		ok := false

		for _, a := range allowed {
			if d.Path.Within(a) {
				ok = true

				break
			}
		}

		if !ok {
			return fmt.Errorf("the field %q is not one of limits.fields: %w", d.Path, ErrLimit)
		}
	}

	return nil
}

// Edit sets one field of a definition to a value, or removes it when Value
// is nil.
type Edit struct {
	Path  Path
	Value json.RawMessage
}

// Apply returns doc, a JSON object, with the edits made in order. A missing
// object on the way to a field is made. It fails for a path that goes into
// something that is not an object or array, for a position past the end of
// an array, and for removing an array element.
func Apply(doc json.RawMessage, edits []Edit) (json.RawMessage, error) {
	root, err := decode(doc)
	if err != nil {
		return nil, fmt.Errorf("decode the definition: %w", err)
	}

	for _, e := range edits {
		root, err = set(root, e.Path, e.Value)
		if err != nil {
			return nil, fmt.Errorf("edit %q: %w", e.Path, err)
		}
	}

	out, err := encode(root, "")
	if err != nil {
		return nil, fmt.Errorf("encode the definition: %w", err)
	}

	return out, nil
}

// Revert returns live with every field of the changes put back to its value
// before the change, and removed when it did not exist. Fields the changes
// do not name stay as they are.
func Revert(live json.RawMessage, deltas []Delta) (json.RawMessage, error) {
	edits := make([]Edit, 0, len(deltas))

	for _, d := range deltas {
		edits = append(edits, Edit{Path: d.Path, Value: d.Before})
	}

	return Apply(live, edits)
}

// decode reads a JSON value, keeping numbers as written.
func decode(raw json.RawMessage) (any, error) {
	var v any

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParams, err)
	}

	return v, nil
}

// set returns node with the field at p set to value, or removed when value
// is nil.
func set(node any, p Path, value json.RawMessage) (any, error) {
	if len(p) == 0 {
		if value == nil {
			return nil, fmt.Errorf("%w: the whole definition cannot be removed", ErrPath)
		}

		return decode(value)
	}

	seg := p[0]

	if strings.HasPrefix(seg, "[") {
		return setIndex(node, seg, p[1:], value)
	}

	if node == nil {
		node = map[string]any{}
	}

	obj, ok := node.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not inside an object", ErrPath, seg)
	}

	if len(p) == 1 && value == nil {
		delete(obj, seg)

		return obj, nil
	}

	child, exists := obj[seg]
	if !exists && value == nil {
		return obj, nil
	}

	next, err := set(child, p[1:], value)
	if err != nil {
		return nil, fmt.Errorf("under %q: %w", seg, err)
	}

	obj[seg] = next

	return obj, nil
}

// setIndex is set for a position in an array.
func setIndex(node any, seg string, rest Path, value json.RawMessage) (any, error) {
	list, ok := node.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s is not inside an array", ErrPath, seg)
	}

	n, err := strconv.Atoi(strings.Trim(seg, "[]"))
	if err != nil || n < 0 || n >= len(list) {
		return nil, fmt.Errorf("%w: %s is past the end of an array of %d", ErrPath, seg, len(list))
	}

	if len(rest) == 0 && value == nil {
		return nil, fmt.Errorf("%w: an array element cannot be removed", ErrPath)
	}

	next, err := set(list[n], rest, value)
	if err != nil {
		return nil, fmt.Errorf("under %s: %w", seg, err)
	}

	list[n] = next

	return list, nil
}

// AtAfter reports whether doc has, at every path of the deltas, the value
// the change wrote: the After value, or no field when After is nil. An
// action uses it to find out that its change is in place already, or that
// someone changed one of its fields since.
func AtAfter(doc json.RawMessage, deltas []Delta) (bool, error) {
	return holds(doc, deltas, func(d Delta) json.RawMessage { return d.After })
}

// AtBefore is AtAfter for the value before the change.
func AtBefore(doc json.RawMessage, deltas []Delta) (bool, error) {
	return holds(doc, deltas, func(d Delta) json.RawMessage { return d.Before })
}

// holds reports whether doc has the picked value at every path.
func holds(doc json.RawMessage, deltas []Delta, pick func(Delta) json.RawMessage) (bool, error) {
	root, err := decode(doc)
	if err != nil {
		return false, fmt.Errorf("decode the definition: %w", err)
	}

	for _, d := range deltas {
		got, found := lookup(root, d.Path)

		want := pick(d)
		if want == nil {
			if found {
				return false, nil
			}

			continue
		}

		wantValue, err := decode(want)
		if err != nil {
			return false, fmt.Errorf("decode the value at %q: %w", d.Path, err)
		}

		if !found || !equal(got, wantValue) {
			return false, nil
		}
	}

	return true, nil
}

// lookup returns the value at p, and whether there is one.
func lookup(node any, p Path) (any, bool) {
	for _, seg := range p {
		if strings.HasPrefix(seg, "[") {
			list, ok := node.([]any)
			if !ok {
				return nil, false
			}

			n, err := strconv.Atoi(strings.Trim(seg, "[]"))
			if err != nil || n < 0 || n >= len(list) {
				return nil, false
			}

			node = list[n]

			continue
		}

		obj, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}

		next, ok := obj[seg]
		if !ok {
			return nil, false
		}

		node = next
	}

	return node, true
}

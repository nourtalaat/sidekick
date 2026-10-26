package config

import (
	"fmt"
	"reflect"
	"slices"
)

// found is one value of the type a walk looks for, with its field path.
type found[T any] struct {
	Path  string
	Value T
}

// findAll walks v, a decoded config value, and returns every value of type T
// in it with its field path below path, in a stable order. It looks through
// struct fields (by their YAML keys, as the decoder names them), pointers,
// interfaces, slices and maps, and does not descend into the values it
// finds or into raw YAML nodes. Use it for secret references in decoded
// plugin config structs, whatever their fields are called.
func findAll[T any](v any, path string) []found[T] {
	w := walker[T]{target: reflect.TypeFor[T]()}
	w.walk(reflect.ValueOf(v), path, 0)

	return w.found
}

// walker collects values of type T.
type walker[T any] struct {
	target reflect.Type
	found  []found[T]
}

func (w *walker[T]) walk(v reflect.Value, path string, depth int) {
	if !v.IsValid() || depth > maxDecodeDepth {
		return
	}

	if v.Type() == w.target {
		if !v.CanInterface() {
			return
		}

		w.found = append(w.found, found[T]{Path: path, Value: v.Interface().(T)})

		return
	}

	depth++

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), path, depth)
		}
	case reflect.Struct:
		if v.Type() == nodeType {
			return
		}

		for i := range v.NumField() {
			if f := v.Type().Field(i); f.Type == w.target && isInline(f) {
				w.walk(v.Field(i), path, depth)
			}
		}

		fields := structFields(v.Type())
		for _, key := range fields.keys {
			w.walk(v.FieldByIndex(fields.byKey[key]), joinPath(path, key), depth)
		}

		if fields.inlineMap != nil {
			w.walkMap(v.FieldByIndex(fields.inlineMap), path, depth)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i), depth)
		}
	case reflect.Map:
		w.walkMap(v, path, depth)
	default:
	}
}

// walkMap walks the values of a map in key order.
func (w *walker[T]) walkMap(m reflect.Value, path string, depth int) {
	type entry struct {
		key   string
		value reflect.Value
	}

	entries := make([]entry, 0, m.Len())
	for it := m.MapRange(); it.Next(); {
		entries = append(entries, entry{key: fmt.Sprint(it.Key().Interface()), value: it.Value()})
	}

	slices.SortFunc(entries, func(a, b entry) int { return comparePaths(a.key, b.key) })

	for _, e := range entries {
		w.walk(e.value, joinPath(path, e.key), depth)
	}
}

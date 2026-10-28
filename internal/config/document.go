package config

import (
	"encoding"
	"fmt"
	"reflect"
	"time"

	"go.yaml.in/yaml/v3"
)

var (
	sectionType       = reflect.TypeFor[Section]()
	connectorType     = reflect.TypeFor[Connector]()
	webhookType       = reflect.TypeFor[Webhook]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// Document returns v, a decoded config value such as a *Config, a Route
// or a Listener, as plain values for JSON: maps by YAML key, lists,
// strings, numbers and booleans, nil for what is not set. Durations are
// their text, such as "10m0s", secret references stay references with
// their name and key, and each plugin section holds its type key and its
// own keys as the file gave them. Config never holds secret values, so
// neither does the result.
func Document(v any) any {
	return document(reflect.ValueOf(v), 0)
}

func document(v reflect.Value, depth int) any {
	if !v.IsValid() || depth > maxDecodeDepth {
		return nil
	}

	depth++

	switch v.Type() {
	case durationType:
		return time.Duration(v.Int()).String()
	case sectionType:
		return sectionDocument(v.Interface().(Section), typeKey, depth)
	case connectorType:
		return sectionDocument(v.Interface().(Connector).Section, providerKey, depth)
	case webhookType:
		w := v.Interface().(Webhook)
		doc := sectionDocument(w.Section, adapterKey, depth)
		doc["auth"] = document(reflect.ValueOf(w.Auth), depth)
		doc["trusted"] = w.Trusted

		return doc
	case nodeType:
		n := v.Interface().(yaml.Node)

		return nodeDocument(&n, depth)
	}

	if v.Type().Implements(textMarshalerType) && (v.Kind() != reflect.Pointer || !v.IsNil()) {
		if text, err := v.Interface().(encoding.TextMarshaler).MarshalText(); err == nil {
			return string(text)
		}
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}

		return document(v.Elem(), depth)
	case reflect.Struct:
		return structDocument(v, depth)
	case reflect.Map:
		if v.IsNil() {
			return nil
		}

		doc := make(map[string]any, v.Len())
		for it := v.MapRange(); it.Next(); {
			doc[fmt.Sprint(it.Key().Interface())] = document(it.Value(), depth)
		}

		return doc
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}

		doc := make([]any, v.Len())
		for i := range v.Len() {
			doc[i] = document(v.Index(i), depth)
		}

		return doc
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil
	default:
		return v.Interface()
	}
}

// structDocument returns the YAML keys of the struct v with their values.
func structDocument(v reflect.Value, depth int) map[string]any {
	fields := structFields(v.Type())
	doc := make(map[string]any, len(fields.keys))

	for _, key := range fields.keys {
		f := v.FieldByIndex(fields.byKey[key])
		if f.CanInterface() {
			doc[key] = document(f, depth)
		}
	}

	if fields.inlineMap != nil {
		if inline, ok := document(v.FieldByIndex(fields.inlineMap), depth).(map[string]any); ok {
			for k, val := range inline {
				doc[k] = val
			}
		}
	}

	return doc
}

// sectionDocument returns a plugin section: its own keys, and its type
// under typeKey.
func sectionDocument(s Section, typeKey string, depth int) map[string]any {
	doc, ok := nodeDocument(s.Node, depth).(map[string]any)
	if !ok {
		doc = map[string]any{}
	}

	doc[typeKey] = s.Type

	return doc
}

// nodeDocument returns the content of a YAML node, with every mapping's
// keys as strings.
func nodeDocument(n *yaml.Node, depth int) any {
	if n == nil || n.Kind == 0 {
		return nil
	}

	var v any
	if err := n.Decode(&v); err != nil {
		return nil
	}

	return plain(v, depth)
}

// plain turns what yaml decodes into any into values JSON can encode.
func plain(v any, depth int) any {
	if depth > maxDecodeDepth {
		return nil
	}

	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = plain(val, depth+1)
		}

		return x
	case map[any]any:
		doc := make(map[string]any, len(x))
		for k, val := range x {
			doc[fmt.Sprint(k)] = plain(val, depth+1)
		}

		return doc
	case []any:
		for i, val := range x {
			x[i] = plain(val, depth+1)
		}

		return x
	default:
		return x
	}
}

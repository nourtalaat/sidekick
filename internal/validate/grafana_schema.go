package validate

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// fileWhere names the top level of a file in messages.
const fileWhere = "the file"

// schemaLevelSpec names one level of a nested schema: def is its entry in
// $defs, empty for the root, and children is the property that holds the
// list of the next level.
type schemaLevelSpec struct {
	def      string
	children string
}

// schemaLevel checks one level of a document, such as one rule, with each
// of its fields checked on its own, so one bad field does not hide the
// others. The children list is checked without its items, which the next
// level checks one by one.
type schemaLevel struct {
	object   *jsonschema.Resolved
	fields   map[string]*jsonschema.Resolved
	names    []string
	children string
	next     *schemaLevel
}

// problems returns a problem for each field of value that fails the
// schema, then the problems of its children, each one saying where it is,
// for example "groups[0].rules[1]: for: ...".
func (l *schemaLevel) problems(path, where string, value any) []Problem {
	var problems []Problem

	if err := l.object.Validate(value); err != nil {
		problems = append(problems, Problem{Path: path, Message: where + ": " + schemaReason(err)})
	}

	object, ok := value.(map[string]any)
	if !ok {
		return problems
	}

	for _, name := range l.names {
		field, ok := object[name]
		if !ok {
			continue
		}

		if err := l.fields[name].Validate(field); err != nil {
			problems = append(problems, Problem{Path: path, Message: where + ": " + name + ": " + schemaReason(err)})
		}
	}

	if l.next == nil {
		return problems
	}

	list, _ := object[l.children].([]any)
	for i, child := range list {
		problems = append(problems, l.next.problems(path, fmt.Sprintf("%s[%d]", childWhere(where, l.children), i), child)...)
	}

	return problems
}

// loadSchemaLevels parses a JSON Schema and builds its levels, in the
// order given.
func loadSchemaLevels(raw []byte, specs []schemaLevelSpec) (*schemaLevel, error) {
	var root jsonschema.Schema
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse the schema: %w", err)
	}

	var top, previous *schemaLevel

	for _, spec := range specs {
		level, err := newSchemaLevel(&root, spec)
		if err != nil {
			return nil, fmt.Errorf("schema level %q: %w", spec.def, err)
		}

		if previous == nil {
			top = level
		} else {
			previous.next = level
		}

		previous = level
	}

	return top, nil
}

// newSchemaLevel builds one level: a schema with only the type and the
// required fields of the level, and one schema for each of its fields.
func newSchemaLevel(root *jsonschema.Schema, spec schemaLevelSpec) (*schemaLevel, error) {
	def := root
	if spec.def != "" {
		def = root.Defs[spec.def]
		if def == nil {
			return nil, fmt.Errorf("no $defs entry %q", spec.def)
		}
	}

	object, err := resolveWithDefs(root, &jsonschema.Schema{Type: def.Type, Types: def.Types, Required: def.Required})
	if err != nil {
		return nil, fmt.Errorf("object: %w", err)
	}

	level := &schemaLevel{
		object:   object,
		fields:   make(map[string]*jsonschema.Resolved, len(def.Properties)),
		names:    slices.Sorted(maps.Keys(def.Properties)),
		children: spec.children,
	}

	for _, name := range level.names {
		field := def.Properties[name].CloneSchemas()
		if name == spec.children {
			field.Items = nil
		}

		if level.fields[name], err = resolveWithDefs(root, field); err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
	}

	return level, nil
}

// resolveWithDefs resolves s as a schema of its own that can refer to the
// root's $defs.
func resolveWithDefs(root, s *jsonschema.Schema) (*jsonschema.Resolved, error) {
	wrapper := &jsonschema.Schema{Schema: root.Schema, Defs: make(map[string]*jsonschema.Schema, len(root.Defs)), AllOf: []*jsonschema.Schema{s}}
	for name, def := range root.Defs {
		wrapper.Defs[name] = def.CloneSchemas()
	}

	resolved, err := wrapper.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	return resolved, nil
}

// childWhere names a child list for messages: "groups" under the file,
// "groups[0].rules" under a group.
func childWhere(where, children string) string {
	if where == fileWhere {
		return children
	}

	return where + "." + children
}

// schemaReason shortens a schema error, which names every schema it went
// through, to its reason, such as `type: 5 has type "integer", want
// "string"`.
func schemaReason(err error) string {
	message := err.Error()

	i := strings.LastIndex(message, "validating ")
	if i < 0 {
		return message
	}

	j := strings.Index(message[i:], ": ")
	if j < 0 {
		return message
	}

	return message[i+j+2:]
}

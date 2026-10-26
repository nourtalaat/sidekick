package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/nourtalaat/sidekick/internal/source"
)

// DropReason says why a source tool is not shown to the model.
type DropReason string

// Drop reasons.
const (
	// DropDenied: a tools.deny glob matches the tool.
	DropDenied DropReason = "denied"
	// DropNotAllowed: tools.allow is set and no glob in it matches.
	DropNotAllowed DropReason = "not_allowed"
	// DropWrites: the source says the tool can change something.
	DropWrites DropReason = "writes"
	// DropBadName: the qualified name is not one providers accept, for
	// example longer than 64 characters.
	DropBadName DropReason = "bad_name"
	// DropDuplicate: an earlier tool already has the same qualified name.
	DropDuplicate DropReason = "duplicate_name"
	// DropBadSchema: the input schema cannot be used to check input.
	DropBadSchema DropReason = "bad_schema"
)

// Exposed is a source tool the model may call.
type Exposed struct {
	// Name is the qualified name the model sees, such as signoz__search_logs.
	Name string
	// Tool is the tool as the source names and describes it.
	Tool source.Tool

	schema *jsonschema.Resolved
}

// Dropped is a source tool left out, with the reason, for logs, the
// sidekick doctor command and the UI.
type Dropped struct {
	Tool   string
	Reason DropReason
	Detail string
}

// Expose decides which of a source's tools the model may see, in this
// order: the deny globs, then the allow globs, then the read-only rule,
// then tools.rename, then the qualified name, which must fit 64
// characters. Tools keep the source's order.
func Expose(sourceName string, settings source.Settings, tools []source.Tool) ([]Exposed, []Dropped) {
	var (
		exposed []Exposed
		dropped []Dropped
		names   = map[string]bool{}
	)

	for _, t := range tools {
		e, d, ok := expose(sourceName, settings.Tools, t)
		if ok && names[e.Name] {
			d, ok = Dropped{Tool: t.Name, Reason: DropDuplicate, Detail: fmt.Sprintf("another tool is already exposed as %s", e.Name)}, false
		}

		if !ok {
			dropped = append(dropped, d)

			continue
		}

		names[e.Name] = true
		exposed = append(exposed, e)
	}

	return exposed, dropped
}

// expose applies the rules to one tool.
func expose(sourceName string, settings source.ToolSettings, t source.Tool) (Exposed, Dropped, bool) {
	drop := func(reason DropReason, detail string) (Exposed, Dropped, bool) {
		return Exposed{}, Dropped{Tool: t.Name, Reason: reason, Detail: detail}, false
	}

	if kept, err := (source.Filter{Deny: settings.Deny}).Allows(t.Name); err != nil || !kept {
		return drop(DropDenied, "a tools.deny glob matches it")
	}

	if allowed, err := settings.Allows(t.Name); err != nil || !allowed {
		return drop(DropNotAllowed, "no tools.allow glob matches it")
	}

	if !t.ReadOnly {
		return drop(DropWrites, "the source marks it as a tool that can change something")
	}

	name := t.Name
	if renamed, ok := settings.Rename[t.Name]; ok {
		name = renamed
	}

	qualified, err := source.QualifiedName(sourceName, name)
	if err != nil {
		return drop(DropBadName, err.Error()+"; rename it with tools.rename")
	}

	schema, err := resolveSchema(t.InputSchema)
	if err != nil {
		return drop(DropBadSchema, err.Error())
	}

	return Exposed{Name: qualified, Tool: t, schema: schema}, Dropped{}, true
}

// resolveSchema parses and resolves a tool's input schema. An empty schema
// accepts any object.
func resolveSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var s jsonschema.Schema
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("parse input schema: %w", err)
		}
	}

	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve input schema: %w", err)
	}

	return resolved, nil
}

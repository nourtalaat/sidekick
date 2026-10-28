package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// fieldCapsAnswer is the part of a _field_caps answer the source reads:
// for each field, its capabilities under each type it has.
type fieldCapsAnswer struct {
	Fields map[string]map[string]struct {
		Searchable   bool `json:"searchable"`
		Aggregatable bool `json:"aggregatable"`
	} `json:"fields"`
}

// fieldItem is one field in the result of list_fields.
type fieldItem struct {
	Name         string   `json:"name"`
	Types        []string `json:"types"`
	Searchable   bool     `json:"searchable"`
	Aggregatable bool     `json:"aggregatable"`
}

// fieldsResult is the result of list_fields.
type fieldsResult struct {
	Index  string      `json:"index"`
	Fields []fieldItem `json:"fields"`
}

// listFields runs list_fields: the fields of the index pattern, sorted by
// name, without the metadata fields such as _id and without object fields,
// whose leaves are listed on their own.
func (s *Source) listFields(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Index  string `json:"index"`
		Fields string `json:"fields"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	index, err := s.index(in.Index)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: index: %w", err)
	}

	pattern := strings.TrimSpace(in.Fields)
	if pattern == "" {
		pattern = "*"
	}

	if err := checkField(pattern, true); err != nil {
		return source.Result{}, fmt.Errorf("read the input: fields: %w", err)
	}

	var answer fieldCapsAnswer
	if err := s.client.fieldCaps(ctx, index, pattern, &answer); err != nil {
		return source.Result{}, fmt.Errorf("list the fields of %s: %w", index, err)
	}

	out := fieldsResult{Index: index, Fields: []fieldItem{}}
	b := budget{}

	for _, name := range slices.Sorted(maps.Keys(answer.Fields)) {
		if strings.HasPrefix(name, "_") {
			continue
		}

		item := fieldItem{Name: source.CutText(name, maxNameLen)}

		for _, kind := range slices.Sorted(maps.Keys(answer.Fields[name])) {
			if kind == "object" {
				continue
			}

			caps := answer.Fields[name][kind]
			item.Types = append(item.Types, source.CutText(kind, maxNameLen))
			item.Searchable = item.Searchable || caps.Searchable
			item.Aggregatable = item.Aggregatable || caps.Aggregatable
		}

		if len(item.Types) == 0 {
			continue
		}

		if !b.fits(item) {
			break
		}

		out.Fields = append(out.Fields, item)
	}

	empty := fmt.Sprintf("No fields in %s match %s: the pattern matches no index, or no index has such a field.", index, pattern)

	return structured(out, nil, b.cut, len(out.Fields) == 0, empty)
}

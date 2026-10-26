package jsonpath

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

const document = `{
  "alert": {"title": "Disk full", "level": 2, "ok": false, "none": null},
  "labels": {"service": "checkout", "app.kubernetes.io/name": "api", "with \"quote\"": "q"},
  "alerts": [
    {"name": "first", "tags": ["a", "b"]},
    {"name": "second", "tags": []},
    {"other": true}
  ],
  "matrix": [[1, 2], [3]],
  "_private": "p",
  "kebab-case": "k"
}`

func TestFind(t *testing.T) {
	t.Parallel()

	doc := decode(t, document)

	tests := []struct {
		name string
		path string
		want []any
	}{
		{name: "root", path: "$", want: []any{doc}},
		{name: "nested field", path: "$.alert.title", want: []any{"Disk full"}},
		{name: "number keeps its text", path: "$.alert.level", want: []any{json.Number("2")}},
		{name: "false is found", path: "$.alert.ok", want: []any{false}},
		{name: "null is found as nil", path: "$.alert.none", want: []any{nil}},
		{name: "quoted field with dots", path: `$.labels["app.kubernetes.io/name"]`, want: []any{"api"}},
		{name: "quoted field with escapes", path: `$.labels["with \"quote\""]`, want: []any{"q"}},
		{name: "quoted field same as dot field", path: `$["alert"]["title"]`, want: []any{"Disk full"}},
		{name: "field starting with underscore", path: "$._private", want: []any{"p"}},
		{name: "field with a dash", path: "$.kebab-case", want: []any{"k"}},
		{name: "index", path: "$.alerts[1].name", want: []any{"second"}},
		{name: "first index", path: "$.alerts[0].tags[1]", want: []any{"b"}},
		{name: "wildcard keeps array order and skips misses", path: "$.alerts[*].name", want: []any{"first", "second"}},
		{name: "wildcard of wildcard", path: "$.alerts[*].tags[*]", want: []any{"a", "b"}},
		{name: "nested arrays", path: "$.matrix[*][0]", want: []any{json.Number("1"), json.Number("3")}},
		{name: "missing field", path: "$.alert.missing"},
		{name: "field of a string", path: "$.alert.title.more"},
		{name: "field of an array", path: "$.alerts.name"},
		{name: "index past the end", path: "$.alerts[3]"},
		{name: "index of an object", path: "$.alert[0]"},
		{name: "wildcard of an object", path: "$.alert[*]"},
		{name: "wildcard of an empty array", path: "$.alerts[1].tags[*]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := Parse(tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.path, p.String())
			require.Equal(t, tt.want, p.Find(doc))
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantMsg string
	}{
		{name: "empty", path: "", wantMsg: `syntax error in "": must start with $`},
		{name: "no root", path: "alert.title", wantMsg: "must start with $"},
		{name: "text after the root", path: "$alert", wantMsg: `at offset 1: expected . or [, got 'a'`},
		{name: "dot without a name", path: "$.", wantMsg: `expected a field name after "."`},
		{name: "double dot", path: "$..title", wantMsg: `at offset 1: expected a field name after "."`},
		{name: "name starting with a digit", path: "$.1st", wantMsg: `expected a field name after "."`},
		{name: "unclosed bracket", path: "$[", wantMsg: `expected a field name in quotes, an index or "*" after "["`},
		{name: "unclosed index", path: "$[1", wantMsg: `expected "]" after the index`},
		{name: "negative index", path: "$[-1]", wantMsg: `an index or "*"`},
		{name: "index too large", path: "$[99999999999999999999]", wantMsg: "index 99999999999999999999"},
		{name: "unterminated quote", path: `$["title]`, wantMsg: "unterminated field name"},
		{name: "quote without bracket", path: `$["title"`, wantMsg: `expected "]" after the field name`},
		{name: "bad escape", path: `$["\q"]`, wantMsg: `field name "\q"`},
		{name: "single quotes", path: "$['title']", wantMsg: `an index or "*"`},
		{name: "wildcard field", path: "$.*", wantMsg: `expected a field name after "."`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, err := Parse(tt.path)
			require.ErrorIs(t, err, ErrSyntax)
			require.ErrorContains(t, err, tt.wantMsg)
			require.True(t, p.IsZero())
		})
	}
}

func TestUnmarshalText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{name: "valid path", text: "$.alert.title"},
		{name: "invalid path", text: "alert.title", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var p Path

			err := p.UnmarshalText([]byte(tt.text))
			if tt.wantErr {
				require.ErrorIs(t, err, ErrSyntax)
				require.True(t, p.IsZero())

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.text, p.String())
			require.Equal(t, []any{"Disk full"}, p.Find(decode(t, document)))
		})
	}
}

func TestZeroPathFindsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		doc  any
	}{
		{name: "object", doc: map[string]any{"a": "b"}},
		{name: "nil", doc: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var p Path

			require.True(t, p.IsZero())
			require.Empty(t, p.Find(tt.doc))
		})
	}
}

func decode(t *testing.T, s string) any {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()

	var doc any
	require.NoError(t, dec.Decode(&doc))

	return doc
}

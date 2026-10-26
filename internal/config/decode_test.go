package config

import (
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type innerSettings struct {
	Model string `yaml:"model"`
	Level int    `yaml:"level"`
}

type sample struct {
	innerSettings `yaml:",inline"`

	Name     string            `yaml:"name"`
	Count    int               `yaml:"count"`
	Ratio    float64           `yaml:"ratio"`
	Enabled  bool              `yaml:"enabled"`
	Wait     time.Duration     `yaml:"wait"`
	Tags     []string          `yaml:"tags"`
	Labels   map[string]string `yaml:"labels"`
	Child    *sampleChild      `yaml:"child"`
	Children []sampleChild     `yaml:"children"`
	Any      any               `yaml:"any"`
	Raw      yaml.Node         `yaml:"raw"`
	Addr     netip.Addr        `yaml:"addr"`
	Skipped  string            `yaml:"-"`
	NoTag    string
}

type sampleChild struct {
	Size    int    `yaml:"size"`
	Comment string `yaml:"comment"`
}

func (c *sampleChild) setDefaults() {
	c.Size = 7
}

type withInlineMap struct {
	Known string         `yaml:"known"`
	Rest  map[string]int `yaml:",inline"`
}

type withIntKeys struct {
	ByNumber map[int]string `yaml:"byNumber"`
}

type nested struct {
	Next *nested `yaml:"next"`
}

type withEmbeddedNotInline struct {
	sampleChild

	Name string `yaml:"name"`
}

func TestDecoderDecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		yaml     string
		want     sample
		wantErrs Errors
	}{
		{
			name: "every kind of field",
			yaml: `
model: m1
level: 2
name: n
count: 3
ratio: 0.5
enabled: true
wait: 90s
tags: [a, b]
labels: {team: payments}
child: {comment: c}
children: [{size: 1}, {}]
any: {x: [1, 2]}
addr: 10.0.0.1
notag: t
`,
			want: sample{
				innerSettings: innerSettings{Model: "m1", Level: 2},
				Name:          "n", Count: 3, Ratio: 0.5, Enabled: true, Wait: 90 * time.Second,
				Tags:     []string{"a", "b"},
				Labels:   map[string]string{"team": "payments"},
				Child:    &sampleChild{Size: 7, Comment: "c"},
				Children: []sampleChild{{Size: 1}, {Size: 7}},
				Any:      map[string]any{"x": []any{1, 2}},
				Addr:     netip.MustParseAddr("10.0.0.1"),
				NoTag:    "t",
			},
		},
		{
			name:     "unknown key with a suggestion",
			yaml:     "nme: x\n",
			wantErrs: Errors{{Path: "nme", Message: `unknown key (did you mean "name"?)`}},
		},
		{
			name:     "unknown key inside a list item",
			yaml:     "children: [{size: 1}, {sise: 2}]\n",
			want:     sample{Children: []sampleChild{{Size: 1}, {Size: 7}}},
			wantErrs: Errors{{Path: "children[1].sise", Message: `unknown key (did you mean "size"?)`}},
		},
		{
			name:     "tagged out and unexported fields are unknown keys",
			yaml:     "Skipped: x\nhidden: y\n",
			wantErrs: Errors{{Path: "Skipped", Message: "unknown key"}, {Path: "hidden", Message: "unknown key"}},
		},
		{
			name: "wrong scalar types",
			yaml: "count: many\nratio: half\nenabled: maybe\nwait: 10x\n",
			wantErrs: Errors{
				{Path: "count", Message: `expected a whole number, got "many"`},
				{Path: "enabled", Message: `expected true or false, got "maybe"`},
				{Path: "ratio", Message: `expected a number, got "half"`},
				{Path: "wait", Message: `expected a duration such as 30s or 10m, got "10x"`},
			},
		},
		{
			name: "wrong node kinds",
			yaml: "name: [a]\ntags: a\nlabels: [a]\nchild: 3\n",
			want: sample{Child: &sampleChild{Size: 7}},
			wantErrs: Errors{
				{Path: "child", Message: `expected a mapping, got "3"`},
				{Path: "labels", Message: "expected a mapping, got a list"},
				{Path: "name", Message: "expected a string, got a list"},
				{Path: "tags", Message: `expected a list, got "a"`},
			},
		},
		{
			name:     "type with its own unmarshal method",
			yaml:     "addr: not-an-ip\n",
			wantErrs: Errors{{Path: "addr", Message: `invalid value: ParseAddr("not-an-ip"): unable to parse IP`}},
		},
		{
			name:     "key set twice",
			yaml:     "name: a\nname: b\n",
			want:     sample{Name: "a"},
			wantErrs: Errors{{Path: "name", Message: "set more than once"}},
		},
		{
			name:     "key that is not a plain value",
			yaml:     "? [a]\n: x\n",
			wantErrs: Errors{{Path: "", Message: "keys must be plain values, got a list"}},
		},
		{
			name: "null values keep the zero value",
			yaml: "name: ~\nchild:\n",
		},
		{
			name: "aliases and merge keys",
			yaml: `
anchors: &base {size: 2, comment: base}
children:
  - <<: *base
    comment: own
  - *base
  - <<: [{size: 3}, *base]
`,
			wantErrs: Errors{{Path: "anchors", Message: "unknown key"}},
			want: sample{Children: []sampleChild{
				{Size: 2, Comment: "own"},
				{Size: 2, Comment: "base"},
				{Size: 3, Comment: "base"},
			}},
		},
		{
			name:     "merge key with a scalar",
			yaml:     "child:\n  <<: 3\n",
			want:     sample{Child: &sampleChild{Size: 7}},
			wantErrs: Errors{{Path: "child", Message: `a merge key (<<) needs a mapping, got "3"`}},
		},
		{
			name:     "merge key with a list of scalars",
			yaml:     "child:\n  <<: [3]\n",
			want:     sample{Child: &sampleChild{Size: 7}},
			wantErrs: Errors{{Path: "child", Message: `a merge key (<<) needs mappings, got "3"`}},
		},
		{
			name:     "map keys that need quoting in the path",
			yaml:     "labels: {a.b: [x]}\n",
			want:     sample{Labels: map[string]string{"a.b": ""}},
			wantErrs: Errors{{Path: `labels["a.b"]`, Message: "expected a string, got a list"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got sample

			errs := decodeString(t, tt.yaml, &got)
			got.Raw = yaml.Node{}
			require.Equal(t, tt.wantErrs, errs)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDecoderSpecialTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		yaml     string
		target   func() any
		want     any
		wantErrs Errors
	}{
		{
			name:   "inline map collects unknown keys",
			yaml:   "known: k\nextra: 1\nmore: 2\n",
			target: func() any { return &withInlineMap{} },
			want:   &withInlineMap{Known: "k", Rest: map[string]int{"extra": 1, "more": 2}},
		},
		{
			name:     "map keys that are not strings",
			yaml:     "byNumber: {1: a}\n",
			target:   func() any { return &withIntKeys{} },
			want:     &withIntKeys{ByNumber: map[int]string{}},
			wantErrs: Errors{{Path: "byNumber[\"1\"]", Message: "map keys of type int are not supported"}},
		},
		{
			name:     "self-referencing alias stops at the depth limit",
			yaml:     "next: &a {next: *a}\n",
			target:   func() any { return &nested{} },
			wantErrs: Errors{{Path: "next" + strings.Repeat(".next", maxDecodeDepth/2-1), Message: "nested more than 64 levels deep"}},
		},
		{
			name:   "empty document",
			yaml:   "",
			target: func() any { return &withInlineMap{} },
			want:   &withInlineMap{},
		},
		{
			name:     "unexported embedded struct without inline is not a key",
			yaml:     "name: n\nsamplechild: {size: 1}\n",
			target:   func() any { return &withEmbeddedNotInline{} },
			want:     &withEmbeddedNotInline{sampleChild: sampleChild{Size: 7}, Name: "n"},
			wantErrs: Errors{{Path: "samplechild", Message: "unknown key"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target := tt.target()
			errs := decodeString(t, tt.yaml, target)
			require.Equal(t, tt.wantErrs, errs)

			if tt.want != nil {
				require.Equal(t, tt.want, target)
			}
		})
	}
}

func TestDecoderUnderFailed(t *testing.T) {
	t.Parallel()

	d := decoder{errs: &errorList{}, failed: []string{"routes[1]", "model.roles"}}

	tests := []struct {
		path string
		want bool
	}{
		{path: "routes[1]", want: true},
		{path: "routes[1].outputs[0]", want: true},
		{path: "routes[10]", want: false},
		{path: "model.roles.investigate", want: true},
		{path: "model.rolesx", want: false},
		{path: "server", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, d.underFailed(tt.path))
		})
	}
}

func TestJoinPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, path, key, want string
	}{
		{name: "top level", path: "", key: "server", want: "server"},
		{name: "nested", path: "model.connectors", key: "claude", want: "model.connectors.claude"},
		{name: "key with a dot", path: "model.prices", key: "us.model-v1", want: `model.prices["us.model-v1"]`},
		{name: "key with a space", path: "", key: "a b", want: `["a b"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, joinPath(tt.path, tt.key))
		})
	}
}

func TestCleanYAMLError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "type error", err: errors.New("yaml: unmarshal errors:\n  line 3: cannot unmarshal x"), want: "cannot unmarshal x"},
		{name: "plain error", err: errors.New("bad value"), want: "bad value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, cleanYAMLError(tt.err))
		})
	}
}

// decodeString decodes src into target with the strict decoder and returns
// the problems it found.
func decodeString(t *testing.T, src string, target any) Errors {
	t.Helper()

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))

	var errs errorList

	d := decoder{errs: &errs}
	d.decode(&doc, reflect.ValueOf(target).Elem(), "")

	return errs.items.sorted()
}

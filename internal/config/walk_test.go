package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

type walkHeader struct {
	Secret *registry.SecretRef `yaml:"secret"`
	Prefix string              `yaml:"prefix"`
}

type walkApp struct {
	PrivateKey registry.SecretRef `yaml:"privateKeySecret"`
}

type walkEmbedded struct {
	Token registry.SecretRef `yaml:"token"`
}

type walkConfig struct {
	walkEmbedded `yaml:",inline"`

	Credentials registry.SecretRef            `yaml:"credentials"`
	Unset       *registry.SecretRef           `yaml:"unset"`
	Headers     map[string]walkHeader         `yaml:"headers"`
	Apps        []walkApp                     `yaml:"apps"`
	Fixed       [1]walkApp                    `yaml:"fixed"`
	Nested      *walkConfig                   `yaml:"nested"`
	Any         any                           `yaml:"any"`
	Raw         *yaml.Node                    `yaml:"raw"`
	Extra       map[string]registry.SecretRef `yaml:",inline"`
	Ignored     registry.SecretRef            `yaml:"-"`
	NoTag       registry.SecretRef
}

func TestFindAllSecretRefs(t *testing.T) {
	t.Parallel()

	ref := func(name string) registry.SecretRef { return registry.SecretRef{Name: name, Key: "k"} }
	refPtr := func(name string) *registry.SecretRef { r := ref(name); return &r }

	tests := []struct {
		name  string
		value any
		want  []found[registry.SecretRef]
	}{
		{name: "nil", value: nil, want: nil},
		{name: "a reference itself", value: ref("a"), want: []found[registry.SecretRef]{{Path: "sources.x", Value: ref("a")}}},
		{
			name: "fields, pointers, maps, lists and nested structs",
			value: &walkConfig{
				walkEmbedded: walkEmbedded{Token: ref("token")},
				Credentials:  ref("creds"),
				Headers: map[string]walkHeader{
					"X-Key":         {Secret: refPtr("x")},
					"Authorization": {Secret: refPtr("auth"), Prefix: "Bearer "},
					"Plain":         {Prefix: "p"},
				},
				Apps:   []walkApp{{PrivateKey: ref("app0")}, {PrivateKey: ref("app1")}},
				Fixed:  [1]walkApp{{PrivateKey: ref("fixed")}},
				Nested: &walkConfig{Credentials: ref("inner")},
				Any:    walkApp{PrivateKey: ref("any")},
				Raw:    &yaml.Node{Kind: yaml.ScalarNode, Value: "name"},
				Extra:  map[string]registry.SecretRef{"b.c": ref("extra-b"), "a": ref("extra-a")},
				NoTag:  ref("notag"),
			},
			want: []found[registry.SecretRef]{
				{Path: "sources.x.token", Value: ref("token")},
				{Path: "sources.x.credentials", Value: ref("creds")},
				{Path: "sources.x.headers.Authorization.secret", Value: ref("auth")},
				{Path: "sources.x.headers.X-Key.secret", Value: ref("x")},
				{Path: "sources.x.apps[0].privateKeySecret", Value: ref("app0")},
				{Path: "sources.x.apps[1].privateKeySecret", Value: ref("app1")},
				{Path: "sources.x.fixed[0].privateKeySecret", Value: ref("fixed")},
				{Path: "sources.x.nested.credentials", Value: ref("inner")},
				{Path: "sources.x.nested.fixed[0].privateKeySecret", Value: registry.SecretRef{}},
				{Path: "sources.x.nested.notag", Value: registry.SecretRef{}},
				{Path: "sources.x.nested.token", Value: registry.SecretRef{}},
				{Path: "sources.x.any.privateKeySecret", Value: ref("any")},
				{Path: "sources.x.notag", Value: ref("notag")},
				{Path: "sources.x.a", Value: ref("extra-a")},
				{Path: `sources.x["b.c"]`, Value: ref("extra-b")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := findAll[registry.SecretRef](tt.value, "sources.x")
			require.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestFindAllOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value map[string]registry.SecretRef
		want  []string
	}{
		{name: "map keys in path order", value: map[string]registry.SecretRef{"b": {}, "a10": {}, "a2": {}}, want: []string{"x.a2", "x.a10", "x.b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var paths []string
			for _, f := range findAll[registry.SecretRef](tt.value, "x") {
				paths = append(paths, f.Path)
			}

			require.Equal(t, tt.want, paths)
		})
	}
}

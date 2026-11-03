package gitlab

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{name: "an exact path", pattern: "acme/shop", path: "acme/shop", want: true},
		{name: "a star in one part", pattern: "acme/*", path: "acme/shop", want: true},
		{name: "a star does not cross a slash", pattern: "acme/*", path: "acme/sub/shop", want: false},
		{name: "a star in a part of a name", pattern: "acme/shop-*", path: "acme/shop-api", want: true},
		{name: "a question mark is one character", pattern: "acme/shop-?", path: "acme/shop-a", want: true},
		{name: "a question mark is not a slash", pattern: "acme?shop", path: "acme/shop", want: false},
		{name: "two stars match deeper paths", pattern: "acme/**", path: "acme/a/b/c", want: true},
		{name: "two stars match one part", pattern: "acme/**", path: "acme/a", want: true},
		{name: "two stars in the middle", pattern: "acme/**/api", path: "acme/a/b/api", want: true},
		{name: "two stars in the middle match no part", pattern: "acme/**/api", path: "acme/api", want: true},
		{name: "two stars in the middle need the ending", pattern: "acme/**/api", path: "acme/a/web", want: false},
		{name: "two stars alone match everything", pattern: "**", path: "x/y/z", want: true},
		{name: "another group", pattern: "acme/**", path: "other/a", want: false},
		{name: "a path that is shorter", pattern: "acme/a/b", path: "acme/a", want: false},
		{name: "a path that is longer", pattern: "acme/a", path: "acme/a/b", want: false},
		{name: "a bad pattern matches nothing", pattern: "acme/[x", path: "acme/x", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, matchGlob(tt.pattern, tt.path))
		})
	}
}

func TestCheckProjectGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{name: "a project path", pattern: "acme/shop"},
		{name: "a nested path with wildcards", pattern: "acme/*/shop-?"},
		{name: "two stars alone", pattern: "**"},
		{name: "empty", pattern: "", want: "required"},
		{name: "one part", pattern: "acme", want: `"acme" must be a path such as group/project, with optional wildcards`},
		{name: "a trailing slash", pattern: "acme/", want: `"acme/" must be a path such as group/project, without an empty part`},
		{name: "two stars inside a name", pattern: "acme/a**", want: `"acme/a**": ** must be a whole part of the path`},
		{name: "a character a path cannot hold", pattern: "acme/a b", want: `"acme/a b" must be a path such as group/project, with optional wildcards`},
		{name: "an unclosed bracket", pattern: "acme/[a", want: `invalid glob "acme/[a": syntax error in pattern`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkProjectGlob(tt.pattern)
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.want)
		})
	}
}

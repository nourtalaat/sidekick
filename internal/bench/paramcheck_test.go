package bench

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		want    []pathStep
		wantErr string
	}{
		{name: "a key", path: "repo", want: []pathStep{{key: "repo"}}},
		{name: "nested keys", path: "spec.title", want: []pathStep{{key: "spec"}, {key: "title"}}},
		{name: "an element", path: "files[0].path", want: []pathStep{{key: "files", indexes: []int{0}}, {key: "path"}}},
		{name: "any element", path: "files[*].path", want: []pathStep{{key: "files", indexes: []int{anyElement}}, {key: "path"}}},
		{name: "two indexes", path: "rows[1][*]", want: []pathStep{{key: "rows", indexes: []int{1, anyElement}}}},
		{name: "empty", path: "", wantErr: "the path is empty"},
		{name: "an empty step", path: "files..path", wantErr: `"" is not a key`},
		{name: "an index with no key", path: "[0].path", wantErr: `"[0]" is not a key`},
		{name: "a negative index", path: "files[-1]", wantErr: "is not a key"},
		{name: "an unclosed index", path: "files[0", wantErr: "is not a key"},
		{name: "an index too large", path: "files[99999999999999999999]", wantErr: "too large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePath(tt.path)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHasWildcard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "no index", path: "repo", want: false},
		{name: "an element", path: "files[0].path", want: false},
		{name: "any element", path: "files[*].path", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			steps, err := parsePath(tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.want, hasWildcard(steps))
		})
	}
}

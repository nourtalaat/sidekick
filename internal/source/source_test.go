package source

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterAllows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		filter  Filter
		tool    string
		want    bool
		wantErr error
	}{
		{name: "empty filter allows every tool", filter: Filter{}, tool: "get_pods", want: true},
		{name: "exact allow", filter: Filter{Allow: []string{"get_pods"}}, tool: "get_pods", want: true},
		{name: "glob allow", filter: Filter{Allow: []string{"get_*"}}, tool: "get_events", want: true},
		{name: "not in the allow list", filter: Filter{Allow: []string{"get_*"}}, tool: "delete_pod", want: false},
		{name: "deny only, tool not denied", filter: Filter{Deny: []string{"delete_*"}}, tool: "get_pods", want: true},
		{name: "deny only, tool denied", filter: Filter{Deny: []string{"delete_*"}}, tool: "delete_pod", want: false},
		{
			name:   "deny wins over allow",
			filter: Filter{Allow: []string{"*"}, Deny: []string{"get_secret*"}},
			tool:   "get_secrets",
			want:   false,
		},
		{
			name:    "bad deny pattern",
			filter:  Filter{Deny: []string{"get_["}},
			tool:    "get_pods",
			wantErr: path.ErrBadPattern,
		},
		{
			name:    "bad allow pattern",
			filter:  Filter{Allow: []string{"get_["}},
			tool:    "get_pods",
			wantErr: path.ErrBadPattern,
		},
		{
			name:   "an allow pattern after a match is not checked",
			filter: Filter{Allow: []string{"get_pods", "get_["}},
			tool:   "get_pods",
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := tt.filter.Allows(tt.tool)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.False(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestQualifiedName(t *testing.T) {
	t.Parallel()

	longestSource := "a" + strings.Repeat("b", 23)
	toolRoom := MaxToolNameLen - len("a") - len(Separator)

	tests := []struct {
		name    string
		source  string
		tool    string
		want    string
		wantErr error
	}{
		{name: "simple", source: "k8s", tool: "get_pod_events", want: "k8s__get_pod_events"},
		{name: "source with a hyphen", source: "prom-eu", tool: "query", want: "prom-eu__query"},
		{name: "tool with capitals and hyphens", source: "gh", tool: "List-Commits", want: "gh__List-Commits"},
		{name: "longest source name", source: longestSource, tool: "q", want: longestSource + "__q"},
		{
			name:   "exactly the length limit",
			source: "a",
			tool:   strings.Repeat("t", toolRoom),
			want:   "a__" + strings.Repeat("t", toolRoom),
		},
		{name: "one character over the length limit", source: "a", tool: strings.Repeat("t", toolRoom+1), wantErr: ErrBadName},
		{name: "source name too long", source: longestSource + "c", tool: "q", wantErr: ErrBadName},
		{name: "empty source", source: "", tool: "q", wantErr: ErrBadName},
		{name: "source starts with a digit", source: "1k8s", tool: "q", wantErr: ErrBadName},
		{name: "source with capitals", source: "K8s", tool: "q", wantErr: ErrBadName},
		{name: "source with an underscore", source: "k_8s", tool: "q", wantErr: ErrBadName},
		{name: "empty tool", source: "k8s", tool: "", wantErr: ErrBadName},
		{name: "tool with a dot", source: "k8s", tool: "pods.get", wantErr: ErrBadName},
		{name: "tool with a space", source: "k8s", tool: "get pods", wantErr: ErrBadName},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := QualifiedName(tt.source, tt.tool)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len(got), MaxToolNameLen)
		})
	}
}

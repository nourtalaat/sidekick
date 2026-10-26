package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

func TestVersionCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		info    buildinfo.Info
		args    []string
		want    string
		wantErr string
	}{
		{
			name: "default values when ldflags are not set",
			info: buildinfo.Info{Version: "dev", Commit: "unknown"},
			args: []string{"version"},
			want: "sidekick dev (commit unknown)\n",
		},
		{
			name: "release build",
			info: buildinfo.Info{Version: "v0.1.0", Commit: "4c0db1a"},
			args: []string{"version"},
			want: "sidekick v0.1.0 (commit 4c0db1a)\n",
		},
		{
			name: "build from a commit after a tag",
			info: buildinfo.Info{Version: "v0.1.0-3-g0e67cf8-dirty", Commit: "0e67cf8"},
			args: []string{"version"},
			want: "sidekick v0.1.0-3-g0e67cf8-dirty (commit 0e67cf8)\n",
		},
		{
			name:    "extra arguments are rejected",
			info:    buildinfo.Info{Version: "dev", Commit: "unknown"},
			args:    []string{"version", "extra"},
			wantErr: `unknown command "extra" for "sidekick version"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			root := newRootCmd(tt.info)
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs(tt.args)

			err := root.Execute()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, out.String())
		})
	}
}

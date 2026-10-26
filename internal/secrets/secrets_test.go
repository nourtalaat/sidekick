package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

func TestNewDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{name: "empty uses the chart's mount path", dir: "", want: "/var/run/secrets/sidekick"},
		{name: "Docker and Podman path", dir: "/run/secrets", want: "/run/secrets"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, NewDir(tt.dir).dir)
		})
	}
}

func TestDirResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		ref     registry.SecretRef
		cancel  bool
		want    []byte
		wantErr error
	}{
		{
			name:  "plain file",
			setup: writeSecret("slack", "token", "xoxb-1\n"),
			ref:   registry.SecretRef{Name: "slack", Key: "token"},
			want:  []byte("xoxb-1\n"),
		},
		{
			name:  "Kubernetes layout with relative symbolic links",
			setup: writeKubernetesSecret("slack", "token", "..2026_10_26", "xoxb-2"),
			ref:   registry.SecretRef{Name: "slack", Key: "token"},
			want:  []byte("xoxb-2"),
		},
		{
			name:  "empty file",
			setup: writeSecret("slack", "token", ""),
			ref:   registry.SecretRef{Name: "slack", Key: "token"},
			want:  []byte{},
		},
		{
			name:  "file of exactly the size limit",
			setup: writeSecret("big", "key", strings.Repeat("x", MaxSize)),
			ref:   registry.SecretRef{Name: "big", Key: "key"},
			want:  []byte(strings.Repeat("x", MaxSize)),
		},
		{
			name:    "file over the size limit",
			setup:   writeSecret("big", "key", strings.Repeat("x", MaxSize+1)),
			ref:     registry.SecretRef{Name: "big", Key: "key"},
			wantErr: ErrTooLarge,
		},
		{
			name:    "missing key",
			setup:   writeSecret("slack", "token", "xoxb-1"),
			ref:     registry.SecretRef{Name: "slack", Key: "signing-secret"},
			wantErr: fs.ErrNotExist,
		},
		{
			name: "key is a directory",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, "slack", "token"), dirPerm))
			},
			ref: registry.SecretRef{Name: "slack", Key: "token"},
		},
		{
			name:    "missing secrets directory",
			setup:   func(t *testing.T, dir string) { t.Helper(); require.NoError(t, os.Remove(dir)) },
			ref:     registry.SecretRef{Name: "slack", Key: "token"},
			wantErr: fs.ErrNotExist,
		},
		{
			name: "symbolic link out of the secrets directory",
			setup: func(t *testing.T, dir string) {
				t.Helper()

				outside := filepath.Join(t.TempDir(), "host-file")
				require.NoError(t, os.WriteFile(outside, []byte("not a secret"), filePerm))
				require.NoError(t, os.Mkdir(filepath.Join(dir, "evil"), dirPerm))
				require.NoError(t, os.Symlink(outside, filepath.Join(dir, "evil", "key")))
			},
			ref: registry.SecretRef{Name: "evil", Key: "key"},
		},
		{name: "empty name", ref: registry.SecretRef{Key: "token"}, wantErr: ErrBadRef},
		{name: "empty key", ref: registry.SecretRef{Name: "slack"}, wantErr: ErrBadRef},
		{name: "name is the parent directory", ref: registry.SecretRef{Name: "..", Key: "passwd"}, wantErr: ErrBadRef},
		{name: "key is the current directory", ref: registry.SecretRef{Name: "slack", Key: "."}, wantErr: ErrBadRef},
		{name: "name with a slash", ref: registry.SecretRef{Name: "slack/../other", Key: "token"}, wantErr: ErrBadRef},
		{name: "key with a backslash", ref: registry.SecretRef{Name: "slack", Key: `..\token`}, wantErr: ErrBadRef},
		{
			name:    "cancelled context",
			setup:   writeSecret("slack", "token", "xoxb-1"),
			ref:     registry.SecretRef{Name: "slack", Key: "token"},
			cancel:  true,
			wantErr: context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}

			ctx := t.Context()
			if tt.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				ctx = cancelled
			}

			got, err := NewDir(dir).Resolve(ctx, tt.ref)
			if tt.want == nil {
				require.Error(t, err)
				require.Nil(t, got)
				require.NotContains(t, err.Error(), "not a secret")

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDirResolveReadsRotatedSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(t *testing.T, dir string)
		rotate func(t *testing.T, dir string)
	}{
		{
			name:  "file rewritten in place",
			setup: writeSecret("slack", "token", "old"),
			rotate: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "slack", "token"), []byte("new"), filePerm))
			},
		},
		{
			name:  "Kubernetes swaps the data link",
			setup: writeKubernetesSecret("slack", "token", "..v1", "old"),
			rotate: func(t *testing.T, dir string) {
				t.Helper()

				secret := filepath.Join(dir, "slack")
				require.NoError(t, os.Mkdir(filepath.Join(secret, "..v2"), dirPerm))
				require.NoError(t, os.WriteFile(filepath.Join(secret, "..v2", "token"), []byte("new"), filePerm))
				require.NoError(t, os.Symlink("..v2", filepath.Join(secret, "..data_tmp")))
				require.NoError(t, os.Rename(filepath.Join(secret, "..data_tmp"), filepath.Join(secret, "..data")))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			tt.setup(t, dir)

			resolver := NewDir(dir)
			ref := registry.SecretRef{Name: "slack", Key: "token"}

			got, err := resolver.Resolve(t.Context(), ref)
			require.NoError(t, err)
			require.Equal(t, []byte("old"), got)

			tt.rotate(t, dir)

			got, err = resolver.Resolve(t.Context(), ref)
			require.NoError(t, err)
			require.Equal(t, []byte("new"), got)
		})
	}
}

func TestCheckRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ref     registry.SecretRef
		wantErr string
	}{
		{name: "valid", ref: registry.SecretRef{Name: "sidekick-db", Key: "url"}},
		{name: "key with a dot", ref: registry.SecretRef{Name: "github", Key: "private-key.pem"}},
		{name: "both empty", ref: registry.SecretRef{}, wantErr: "name: required\nkey: required"},
		{name: "name with a slash", ref: registry.SecretRef{Name: "a/b", Key: "k"}, wantErr: `name: "a/b" must not be "." or ".." or contain "/" or "\"`},
		{name: "key is the parent directory", ref: registry.SecretRef{Name: "n", Key: ".."}, wantErr: `key: ".." must not be "." or ".." or contain "/" or "\"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckRef(tt.ref)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestResolveText(t *testing.T) {
	t.Parallel()

	errResolve := errors.New("permission denied")

	tests := []struct {
		name    string
		value   string
		err     error
		want    string
		wantErr error
	}{
		{name: "no newline", value: "xoxb-1", want: "xoxb-1"},
		{name: "trailing newline", value: "xoxb-1\n", want: "xoxb-1"},
		{name: "trailing carriage return and newline", value: "xoxb-1\r\n", want: "xoxb-1"},
		{name: "several trailing newlines", value: "xoxb-1\n\r\n\n", want: "xoxb-1"},
		{name: "inner newlines are kept", value: "line one\nline two\r\nline three\n", want: "line one\nline two\r\nline three"},
		{name: "leading newline and trailing spaces are kept", value: "\n pass \n", want: "\n pass "},
		{name: "only newlines", value: "\r\n\n", want: ""},
		{name: "resolver error", err: errResolve, wantErr: errResolve},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref := registry.SecretRef{Name: "slack", Key: "token"}
			resolver := registry.NewMockSecrets(gomock.NewController(t))
			resolver.EXPECT().Resolve(gomock.Any(), ref).Return([]byte(tt.value), tt.err)

			got, err := ResolveText(t.Context(), resolver, ref)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, got)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// writeSecret returns a setup that writes value to <dir>/<name>/<key>.
func writeSecret(name, key, value string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()

		require.NoError(t, os.MkdirAll(filepath.Join(dir, name), dirPerm))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name, key), []byte(value), filePerm))
	}
}

// writeKubernetesSecret returns a setup that lays out a secret the way the
// kubelet mounts one: <key> links to ..data/<key>, and ..data links to a
// directory named after the version.
func writeKubernetesSecret(name, key, version, value string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()

		secret := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Join(secret, version), dirPerm))
		require.NoError(t, os.WriteFile(filepath.Join(secret, version, key), []byte(value), filePerm))
		require.NoError(t, os.Symlink(version, filepath.Join(secret, "..data")))
		require.NoError(t, os.Symlink(filepath.Join("..data", key), filepath.Join(secret, key)))
	}
}

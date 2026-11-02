package validate

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestReader(t *testing.T) {
	t.Parallel()

	plain := `{"format":"prometheus_rules","files":[{"path":"a.yaml","content":"groups: []\n"}]}`

	tests := []struct {
		name    string
		input   []byte
		want    string
		wantLen int
		wantErr error
	}{
		{name: "plain JSON", input: []byte(plain), want: plain},
		{name: "gzipped JSON", input: gzipped(t, plain), want: plain},
		{name: "empty input", input: nil, want: ""},
		{name: "a gzip header with nothing after it", input: []byte{0x1f, 0x8b, 0x08}, wantErr: ErrBadRequest},
		{name: "gzip that expands past the limit", input: gzipped(t, strings.Repeat("a", MaxRequestBytes+10)), wantLen: MaxRequestBytes},
		{name: "plain input past the limit", input: []byte(strings.Repeat("a", MaxRequestBytes+10)), wantLen: MaxRequestBytes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, err := RequestReader(bytes.NewReader(tt.input))
			if tt.wantErr == nil {
				require.NoError(t, err)
			}

			if err != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			got, err := io.ReadAll(r)
			require.NoError(t, err)

			if tt.wantLen > 0 {
				require.Len(t, got, tt.wantLen)

				return
			}

			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestNewJobError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantKind string
	}{
		{name: "bad request", err: fmt.Errorf("validate: %w", fmt.Errorf("%w: no files", ErrBadRequest)), wantKind: KindBadRequest},
		{name: "timeout", err: fmt.Errorf("validate: %w after 1m0s", ErrTimeout), wantKind: KindTimeout},
		{name: "busy", err: fmt.Errorf("validate: %w", ErrBusy), wantKind: KindBusy},
		{name: "anything else", err: errors.New("open the request: no such file"), wantKind: KindInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := NewJobError(tt.err)
			require.Equal(t, tt.wantKind, got.Kind)
			require.Equal(t, tt.err.Error(), got.Error)
		})
	}

	t.Run("tokens are redacted", func(t *testing.T) {
		t.Parallel()

		got := NewJobError(errors.New("bad value " + fakeJWT))
		require.NotContains(t, got.Error, fakeJWT)
	})
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

package httpclient

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// errTooLarge is the error the tests give LimitBody.
var errTooLarge = errors.New("too large")

func TestLimitBody(t *testing.T) {
	t.Parallel()

	const limit = 5

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "an empty body", body: ""},
		{name: "shorter than the limit", body: "abc"},
		{name: "exactly the limit", body: "abcde"},
		{name: "one byte over the limit", body: "abcdef", wantErr: errTooLarge},
		{name: "far over the limit", body: strings.Repeat("z", 1000), wantErr: errTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := LimitBody(io.NopCloser(strings.NewReader(tt.body)), limit, errTooLarge)

			got, err := io.ReadAll(body)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.LessOrEqual(t, len(got), limit+1)

				_, err = body.Read(make([]byte, 1))
				require.ErrorIs(t, err, tt.wantErr, "every later read fails too")

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.body, string(got))
			require.NoError(t, body.Close())
		})
	}
}

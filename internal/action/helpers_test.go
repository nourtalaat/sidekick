package action

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	type params struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name    string
		raw     string
		want    params
		wantErr string
	}{
		{name: "known keys", raw: `{"name": "ops"}`, want: params{Name: "ops"}},
		{name: "an unknown key", raw: `{"name": "ops", "other": 1}`, wantErr: `decode the parameters: json: unknown field "other"`},
		{name: "not an object", raw: `[1]`, wantErr: "decode the parameters: json: cannot unmarshal array into Go value of type action.params"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got params

			err := DecodeParams(json.RawMessage(tt.raw), &got)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLimitError(t *testing.T) {
	t.Parallel()

	errCause := errors.New("the team is not allowed")

	err := LimitError(errCause, "team %q", "OPS")
	require.EqualError(t, err, `team "OPS": outside configured limits: the team is not allowed`)
	require.ErrorIs(t, err, ErrLimit)
	require.ErrorIs(t, err, errCause)
}

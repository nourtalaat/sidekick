package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
)

func TestText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "disk full", want: "disk full"},
		{name: "NUL characters", in: "a\x00b\x00", want: "a�b�"},
		{name: "invalid UTF-8", in: "a\xff\xfeb", want: "a�b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, store.Text(tt.in))
			require.True(t, store.ValidText(store.Text(tt.in)))
		})
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "no NUL escape", in: `{"a":"b\n"}`, want: `{"a":"b\n"}`},
		{name: "NUL escape in a value", in: `{"a":"x\u0000y"}`, want: `{"a":"x�y"}`},
		{name: "NUL escape in a key", in: `{"\u0000":1}`, want: `{"�":1}`},
		{name: "escaped backslash before u0000 is text", in: `{"a":"\\u0000"}`, want: `{"a":"\\u0000"}`},
		{name: "escaped backslash then a NUL escape", in: `["\\\u0000"]`, want: `["\\�"]`},
		{name: "several escapes", in: `["\u0000\"\u0000"]`, want: `["�\"�"]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, string(store.JSON([]byte(tt.in))))
		})
	}
}

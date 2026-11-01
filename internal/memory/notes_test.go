package memory

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
		want    string
		invalid bool
	}{
		{name: "a name", service: "checkout-api", want: "checkout-api"},
		{name: "trimmed", service: "  checkout-api\n", want: "checkout-api"},
		{name: "a name with a slash", service: "team/checkout", want: "team/checkout"},
		{name: "empty", service: " ", invalid: true},
		{name: "too long", service: strings.Repeat("a", MaxServiceChars+1), invalid: true},
		{name: "the longest", service: strings.Repeat("é", MaxServiceChars), want: strings.Repeat("é", MaxServiceChars)},
		{name: "on two lines", service: "a\nb", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := checkService(tt.service)
			if tt.invalid {
				invalid, ok := errors.AsType[*InvalidError](err)
				require.True(t, ok, "want an *InvalidError, got %v", err)
				require.Equal(t, "service", invalid.Field)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckNotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		want    string
		invalid bool
	}{
		{name: "text is trimmed", text: "\n Restart the pool first.  ", want: "Restart the pool first."},
		{name: "empty text is allowed, it deletes", text: "  ", want: ""},
		{name: "lines and tabs are allowed", text: "a\n\tb", want: "a\n\tb"},
		{name: "the longest", text: strings.Repeat("é", MaxNotesChars), want: strings.Repeat("é", MaxNotesChars)},
		{name: "too long", text: strings.Repeat("a", MaxNotesChars+1), invalid: true},
		{name: "control characters", text: "a\x07b", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := (&Store{}).checkNotes(tt.text)
			if tt.invalid {
				invalid, ok := errors.AsType[*InvalidError](err)
				require.True(t, ok, "want an *InvalidError, got %v", err)
				require.Equal(t, "text", invalid.Field)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

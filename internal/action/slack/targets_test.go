package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTargetsParseExternal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		external string
		want     Target
		wantOK   bool
	}{
		{name: "a Slack thread", external: "C0123456789:1700000000.000100", want: Target{Channel: "C0123456789", TS: "1700000000.000100"}, wantOK: true},
		{name: "a Teams thread", external: "19:abc@thread.tacv2;messageid=1"},
		{name: "no timestamp", external: "C0123456789:"},
		{name: "not a channel", external: "web:1700000000.000100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := parseExternal(tt.external)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

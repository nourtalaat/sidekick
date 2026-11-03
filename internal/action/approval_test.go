package action

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type gatedConfig struct {
	ReadWriteGate
	Name string
}

type plainConfig struct{ Name string }

func TestReadWriteBlocked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		want bool
	}{
		{name: "nil", v: nil, want: false},
		{name: "a value that is not approval-only", v: plainConfig{}, want: false},
		{name: "a pointer to a value that is not approval-only", v: &plainConfig{}, want: false},
		{name: "the gate with no opt-in", v: ReadWriteGate{}, want: true},
		{name: "the gate with the opt-in", v: ReadWriteGate{AllowReadWrite: true}, want: false},
		{name: "a config that embeds the gate", v: gatedConfig{}, want: true},
		{name: "a pointer to a config that embeds the gate", v: &gatedConfig{}, want: true},
		{name: "a pointer to a config that opted in", v: &gatedConfig{ReadWriteGate: ReadWriteGate{AllowReadWrite: true}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ReadWriteBlocked(tt.v))
		})
	}
}

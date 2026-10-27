package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "a plain error", err: errors.New("failed"), want: exitFailure},
		{name: "bad input", err: &exitError{code: exitBadInput, err: errors.New("bad")}, want: exitBadInput},
		{name: "a wrapped exit error", err: fmt.Errorf("run: %w", &exitError{code: exitInfrastructure, err: errors.New("down")}), want: exitInfrastructure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, exitCode(tt.err))
		})
	}
}

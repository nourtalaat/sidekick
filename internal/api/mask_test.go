package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMask(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in, want string
	}{
		{in: "https://user:token@mcp.internal/mcp", want: "https://mcp.internal/mcp"}, //nolint:gosec // Fake credentials that mask must remove.
		{in: "https://token@mcp.internal/", want: "https://mcp.internal/"},
		{in: "postgres://sidekick:pw@db:5432/sidekick?sslmode=require", want: "postgres://db:5432/sidekick?sslmode=require"},       //nolint:gosec // Fake credentials that mask must remove.
		{in: "dial postgres://a:b@db:5432 failed; retry https://x:y@h/", want: "dial postgres://db:5432 failed; retry https://h/"}, //nolint:gosec // Fake credentials that mask must remove.
		{in: "https://mcp.internal/mcp?user=a@b", want: "https://mcp.internal/mcp?user=a@b"},
		{in: "ana@example.com", want: "ana@example.com"},
		{in: "plain text", want: "plain text"},
	}

	s := &server{}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, s.mask(tt.in))
		})
	}
}

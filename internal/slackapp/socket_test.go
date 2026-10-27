package slackapp

import (
	"errors"
	"fmt"
	"testing"

	"github.com/slack-go/slack"
	"github.com/stretchr/testify/require"
)

func TestRefusedToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "invalid_auth", err: fmt.Errorf("run: %w", slack.SlackErrorResponse{Err: "invalid_auth"}), want: true},
		{name: "a revoked token", err: slack.SlackErrorResponse{Err: "token_revoked"}, want: true},
		{name: "another Slack error", err: slack.SlackErrorResponse{Err: "ratelimited"}},
		{name: "another error", err: errors.New("dial tcp: refused")},
		{name: "no error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, refusedToken(tt.err))
		})
	}
}

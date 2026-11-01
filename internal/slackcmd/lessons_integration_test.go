//go:build integration

package slackcmd_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLessonAddRedactsTheText(t *testing.T) {
	t.Parallel()

	h := setup(t)

	status, reply := h.command(t, admin, `lesson add "Ask ana@example.com about the deploy." service:checkout-api`)
	require.Equal(t, 200, status)
	require.NotContains(t, reply, "ana@example.com")
	require.Contains(t, reply, "[REDACTED:")

	var text string

	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT text FROM lessons").Scan(&text))
	require.NotContains(t, text, "ana@example.com")
	require.Contains(t, text, "[REDACTED:")
}

func TestLessonAdd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user string
		text string
		// wantLessons is how many lessons are stored in the end, and
		// wantReply is in the reply.
		wantLessons int
		wantReply   string
	}{
		{
			name:        "an admin writes an active lesson with a scope",
			user:        admin,
			text:        `lesson add "Check the deploys of the last two hours first." service:checkout-api alert:CheckoutLatencyP99`,
			wantLessons: 1,
			wantReply:   "Lesson added for service checkout-api, alert CheckoutLatencyP99: Check the deploys of the last two hours first.",
		},
		{
			name:      "a responder may not",
			user:      responder,
			text:      `lesson add "Check the deploys first." service:checkout-api`,
			wantReply: "This needs the admin role, and you are a responder.",
		},
		{
			name:      "a lesson with no scope is not written from Slack",
			user:      admin,
			text:      `lesson add "Check the deploys first."`,
			wantReply: "A lesson from Slack needs at least one scope option.",
		},
		{
			name:      "a text that is too long",
			user:      admin,
			text:      `lesson add "` + strings.Repeat("a", 401) + `" service:checkout-api`,
			wantReply: "The text is longer than 400 characters.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)

			status, reply := h.command(t, tt.user, tt.text)
			require.Equal(t, 200, status)
			require.Contains(t, reply, tt.wantReply)

			var n int

			require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM lessons").Scan(&n))
			require.Equal(t, tt.wantLessons, n)

			if tt.wantLessons == 0 {
				return
			}

			var (
				status2, origin, service, alert string
				createdBy                       *string
				audits                          int
			)

			require.NoError(t, h.pool.QueryRow(t.Context(),
				"SELECT status, origin, scope_service, scope_alert, created_by::text FROM lessons").Scan(&status2, &origin, &service, &alert, &createdBy))
			require.Equal(t, "active", status2)
			require.Equal(t, "slack", origin)
			require.Equal(t, "checkout-api", service)
			require.Equal(t, "CheckoutLatencyP99", alert)
			require.Nil(t, createdBy, "a Slack user is not a Sidekick user")

			require.NoError(t, h.pool.QueryRow(t.Context(),
				"SELECT count(*) FROM audit_log WHERE action = 'lesson.created' AND actor_id = $1", tt.user).Scan(&audits))
			require.Equal(t, 1, audits)
		})
	}
}

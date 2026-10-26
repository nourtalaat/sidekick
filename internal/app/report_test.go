package app

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReport(t *testing.T) {
	t.Parallel()

	// pgx joins one error per address it tried, as it does for localhost.
	multiLine := errors.Join(
		errors.New("failed to connect to `user=sidekick database=sidekick`:"),
		errors.New("\t[::1]:5432 (localhost): dial error: connection refused"),
		errors.New("\t127.0.0.1:5432 (localhost): dial error: connection refused"),
	)

	tests := []struct {
		name       string
		checks     []check
		wantStatus int
		wantBody   string
	}{
		{
			name: "every check passes",
			checks: []check{
				{name: "config", ok: true, message: "ok"},
				{name: "database", ok: true, message: "ok"},
			},
			wantStatus: http.StatusOK,
			wantBody:   "config: ok\ndatabase: ok\n",
		},
		{
			name: "multi-line error",
			checks: []check{
				{name: "config", ok: true, message: "ok"},
				{name: "database", message: "unreachable: " + multiLine.Error()},
				{name: "schema", message: "not checked, because the database is unreachable"},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody: "config: ok\n" +
				"database: unreachable: failed to connect to `user=sidekick database=sidekick`:; " +
				"[::1]:5432 (localhost): dial error: connection refused; " +
				"127.0.0.1:5432 (localhost): dial error: connection refused\n" +
				"schema: not checked, because the database is unreachable\n",
		},
		{
			name: "blank lines and trailing newline",
			checks: []check{
				{name: "database", message: "first\r\n\n  second\n"},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "database: first; second\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, body := report(tt.checks)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantBody, body)
		})
	}
}

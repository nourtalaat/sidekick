//go:build integration

package bench

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// stoppedTimeout bounds the connection attempt to a stopped Postgres.
const stoppedTimeout = 5 * time.Second

func TestStartTemporary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "starts, migrates and stops"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, err := StartTemporary(t.Context(), nil)
			require.NoError(t, err)

			closed := false

			t.Cleanup(func() {
				if !closed {
					require.NoError(t, d.Close(context.WithoutCancel(t.Context())))
				}
			})

			require.Empty(t, d.Schema)

			var version int64
			require.NoError(t, d.Pool.QueryRow(t.Context(), "SELECT max(version_id) FROM goose_db_version").Scan(&version))
			require.Positive(t, version, "the migrations are applied")

			url := d.Pool.Config().ConnString()

			require.NoError(t, d.Close(t.Context()))

			closed = true

			ctx, cancel := context.WithTimeout(t.Context(), stoppedTimeout)
			defer cancel()

			conn, err := pgx.Connect(ctx, url)
			if err == nil {
				_ = conn.Close(ctx)
			}

			require.Error(t, err, "the container is gone")
		})
	}
}

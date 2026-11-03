//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// checkViolation is the SQLSTATE of a row that fails a CHECK constraint.
const checkViolation = "23514"

// TestUserThemeColumn checks that users.theme takes null and the three
// themes, and rejects any other value.
func TestUserThemeColumn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		theme   *string
		wantErr bool
	}{
		{name: "never chosen", theme: nil},
		{name: "system", theme: new("system")},
		{name: "light", theme: new("light")},
		{name: "dark", theme: new("dark")},
		{name: "another theme", theme: new("purple"), wantErr: true},
		{name: "an empty string", theme: new(""), wantErr: true},
		{name: "a different case", theme: new("Dark"), wantErr: true},
	}

	pool := storetest.New(t)

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := pool.Exec(t.Context(),
				`INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, theme, created_at, last_login_at)
				 VALUES ($1, 'https://idp.example.com', $2, 'Vi', 'viewer', $3, now(), now())`,
				fmt.Sprintf("01920000-0000-7000-8000-%012d", i+1), fmt.Sprintf("user-%d", i), tt.theme)

			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			require.True(t, ok, "want a Postgres error, got %v", err)
			require.Equal(t, checkViolation, pgErr.Code)
			require.Equal(t, "users_theme_valid", pgErr.ConstraintName)
		})
	}
}

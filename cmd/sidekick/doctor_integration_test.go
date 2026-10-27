//go:build integration

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestDoctorDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		dbURL   func(t testing.TB) string
		wantErr bool
		want    string
	}{
		{
			name:  "a migrated database",
			dbURL: storetest.NewURL,
			want:  "ok    database: connected; the schema has every migration this release needs",
		},
		{
			name:    "a database without migrations",
			dbURL:   storetest.NewEmptyURL,
			wantErr: true,
			want:    "FAIL  database: connected, but the database has migrations up to 0 and this release needs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			root := newTestRootCmd(newDoctorCmd(fakeRegistries()))
			root.SetOut(&out)
			root.SetErr(&bytes.Buffer{})
			root.SetArgs([]string{"doctor", "-c", writeConfig(t, validConfig, tt.dbURL(t))})

			err := root.Execute()
			if tt.wantErr {
				require.ErrorIs(t, err, errDoctorFailed)
			} else {
				require.NoError(t, err)
			}

			require.Contains(t, out.String(), tt.want)
		})
	}
}

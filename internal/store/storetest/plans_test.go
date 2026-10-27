//go:build integration

package storetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWritePlanFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		testName string
		wantPath string
	}{
		{name: "top-level test", testName: "TestQueryPlans", wantPath: "TestQueryPlans.txt"},
		{name: "subtest", testName: "TestQueryPlans/ClaimJobs", wantPath: "TestQueryPlans/ClaimJobs.txt"},
		{
			name:     "unsafe characters",
			testName: "TestQueryPlans/lock_waits:_waiting_sessions?#01",
			wantPath: "TestQueryPlans/lock_waits__waiting_sessions_01.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			require.NoError(t, writePlanFile(dir, tt.testName, "\n-- name: GetJob :one\nSELECT 1\n", "Result"))

			got, err := os.ReadFile(filepath.Clean(filepath.Join(dir, filepath.FromSlash(tt.wantPath))))
			require.NoError(t, err)
			require.Equal(t, "-- name: GetJob :one\nSELECT 1\n\nResult\n", string(got))
		})
	}
}

func TestPackageDir(t *testing.T) {
	t.Parallel()

	got, err := packageDir()
	require.NoError(t, err)
	require.Equal(t, "internal/store/storetest", got)
}

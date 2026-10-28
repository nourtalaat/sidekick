//go:build integration

package audit_test

import (
	"os"
	"testing"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

//go:build integration

package store_test

import (
	"os"
	"testing"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

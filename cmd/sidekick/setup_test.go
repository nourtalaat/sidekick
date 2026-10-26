//go:build integration

package main

import (
	"os"
	"testing"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

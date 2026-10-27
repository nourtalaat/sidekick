// Command sidekick investigates alerts.
package main

import (
	"os"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

func main() {
	if err := newRootCmd(buildinfo.Current()).Execute(); err != nil {
		os.Exit(exitCode(err))
	}
}

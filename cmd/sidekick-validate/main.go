// Command sidekick-validate checks monitor definition files before Sidekick
// opens a pull request that changes them. It runs in the validator image,
// as a sidecar next to Sidekick or as a Kubernetes Job.
package main

import (
	"os"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

func main() {
	if err := newRootCmd(buildinfo.Current()).Execute(); err != nil {
		os.Exit(1)
	}
}

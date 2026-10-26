// Package buildinfo holds the version and commit that make build sets
// with -ldflags.
package buildinfo

// Version and Commit are set at build time with -ldflags -X.
var (
	Version = "dev"
	Commit  = "unknown"
)

// Info describes the running build.
type Info struct {
	Version string
	Commit  string
}

// Current returns the version and commit of the running binary.
func Current() Info {
	return Info{Version: Version, Commit: Commit}
}

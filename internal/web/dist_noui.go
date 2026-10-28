//go:build noui

package web

import "io/fs"

// embeddedSite is nil under the noui tag, so the handler serves the page
// for builds without the UI, and `go build -tags noui` needs no Node.
func embeddedSite() fs.FS {
	return nil
}

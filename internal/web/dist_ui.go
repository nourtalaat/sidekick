//go:build !noui

package web

import (
	"embed"
	"io/fs"
)

// dist is the UI that `make ui-build` writes. Without a build it holds
// only .gitkeep, and the handler then serves the page for builds without
// the UI.
//
//go:embed all:dist
var dist embed.FS

func embeddedSite() fs.FS {
	site, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}

	return site
}

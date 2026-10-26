// Package strategies holds the built-in investigation strategies. They are
// embedded in the binary; internal/strategy loads them together with the
// file strategies from strategies.dir.
package strategies

import "embed"

// FS holds one YAML file per built-in strategy.
//
//go:embed *.yaml
var FS embed.FS

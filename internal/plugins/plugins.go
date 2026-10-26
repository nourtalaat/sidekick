// Package plugins links every built-in plugin into the binary. Each plugin
// package registers itself with the registry in its init function, so this
// package holds only blank imports of them. Import it once from the command
// that builds the configured instances.
package plugins

import (
	_ "github.com/nourtalaat/sidekick/internal/intake/alertmanager" // Intake adapter "alertmanager".
	_ "github.com/nourtalaat/sidekick/internal/intake/generic"      // Intake adapter "generic".
	_ "github.com/nourtalaat/sidekick/internal/model/anthropic"     // Model provider "anthropic".
	_ "github.com/nourtalaat/sidekick/internal/source/mcp"          // Source type "mcp".
)

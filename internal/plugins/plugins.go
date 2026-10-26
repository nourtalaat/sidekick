// Package plugins links every built-in plugin into the binary. Each plugin
// package registers itself with the registry in its init function, so this
// package holds only blank imports of them. Import it once from the command
// that builds the configured instances.
package plugins

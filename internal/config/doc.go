// Package config loads sidekick.yaml, checks it, and builds the configured
// plugin instances through the registry.
//
// Loading is strict: an unknown key anywhere is an error, including inside
// plugin sections. Every problem is reported at once, each with the path of
// the field it is about and, for misspelled names, a suggestion.
package config

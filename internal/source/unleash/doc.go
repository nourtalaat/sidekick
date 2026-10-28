// Package unleash is the native Unleash source. It registers itself as the
// source type "unleash".
//
// A source reads one Unleash instance through its Admin API with a service
// account token, and only reads. Its two tools, from package flags, list
// the changes to feature flags in a project and environment from the
// event log, and get one flag's current state in an environment. A call
// for a project or environment the config does not list is a tool error
// before anything is sent, and every result links to the flag's page in
// the Unleash UI.
package unleash

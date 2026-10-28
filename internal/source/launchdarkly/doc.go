// Package launchdarkly is the native LaunchDarkly source. It registers
// itself as the source type "launchdarkly".
//
// A source reads one LaunchDarkly account through its REST API with an
// access token, and only reads. Its two tools, from package flags, list
// the changes to feature flags in a project and environment from the
// audit log, and get one flag's current state in an environment. A call
// for a project or environment the config does not list is a tool error
// before anything is sent, and every result links to the flag's page in
// LaunchDarkly.
package launchdarkly

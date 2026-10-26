// Package watch runs the checks of watchers: periodic, read-only checks
// that tell when a service is back up after an alert.
//
// A check is one of three types. An HTTP check GETs a URL on an allowed
// host through a client that cannot reach private or special addresses
// unless they are allowed. A tool check calls a source tool with fixed
// arguments and tests a condition on the result. An alert check passes when
// the sender's recovery events have arrived for the group. Each check ends
// as pass, fail or error, with a short redacted detail.
//
// HTTP and tool checks run through the source guard of
// internal/investigator/tools, so they get the same timeout, concurrency
// limit, circuit breaker and redaction as investigation tool calls.
package watch

// Package router runs route_event jobs: it matches each stored event to the
// first route that fits it and hands it to the grouper, which joins it to
// the open group with its fingerprint or opens a new one. For a new group
// it creates a queued investigation, with the investigate job and the first
// publish job of each of the route's outputs, in the same transaction.
//
// Routes are tried by priority, lower first, then by name, and the default
// route, which matches every event, is always last. A route matches when
// every field of its match that is set fits the event: the intake, the
// labels, a glob on the service, the severity, and a regular expression on
// the title and the body joined by a newline. Matching runs on the stored
// event, which intake has already redacted.
package router

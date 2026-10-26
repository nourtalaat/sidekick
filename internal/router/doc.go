// Package router runs route_event jobs: it matches each stored event to the
// first route that fits it and opens an event group and a queued
// investigation for it, with the investigate job, in one transaction.
//
// Routes are tried by priority, lower first, then by name, and the default
// route, which matches every event, is always last. A route matches when
// every field of its match that is set fits the event: the intake, the
// labels, a glob on the service, the severity, and a regular expression on
// the title and the body joined by a newline. Matching runs on the stored
// event, which intake has already redacted.
//
// For now every routed event opens a group of its own, even when an open
// group has the same fingerprint.
package router

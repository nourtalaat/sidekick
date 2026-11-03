// Package mute holds what the actions that mute an alert for a limited
// time have in common: how long a mute may last, the reason text and the
// marker that lets a retry or an Undo find the mute again, and the scopes
// that say which label matchers may be muted. alertmanager.silence uses all
// of it; the actions for Grafana and Datadog use the parts that fit their
// API.
//
// A mute always starts when it is created. None of the mute APIs has an
// idempotency key, which would let a server recognise a repeated request,
// so the marker in the reason text does that job: Execute looks for a mute
// with the marker of its proposal before it creates one, and Undo ends the
// mute that has it.
package mute

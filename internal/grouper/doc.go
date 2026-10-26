// Package grouper puts events into event groups, so that one group, with
// one investigation and one message in each output, covers every event of
// the same alert.
//
// The router calls Group in the transaction that routes an event. The
// event joins the open group with its fingerprint, or, when there is none,
// opens a new group, and the router then creates the group's investigation
// in the same transaction. At most one group per fingerprint is open, which
// a unique index on open groups makes sure of across replicas: when two
// transactions open a group for one fingerprint at once, the second waits
// for the first and then joins its group.
//
// A joined event counts toward the group's event count, moves the end of
// its window forward, goes to the investigation's inbox, and refreshes what
// the outputs show, so the message has the new count. A group whose window
// ended without anyone closing it yet, and that runs no watcher, is closed
// as window_ended when the next event arrives, and that event opens a new
// group.
//
// The fingerprint of an alert under a route with fingerprint labels is made
// from the intake, the alert name and the values of those labels, with a
// missing label counted as empty. Without such labels, for an event that is
// not an alert, or for an alert that has none of the labels, it is the
// fingerprint the intake stored, which names one alert.
package grouper

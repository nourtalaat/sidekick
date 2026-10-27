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
// A route with a related window also lets an event that finds no open
// group with its fingerprint join the route's open group of the same
// service, or of a service it depends on or that depends on it, when that
// group's newest event is within the related window. The Catalog knows the
// dependencies: it reads the sources that are service catalogs in the
// background and keeps them in memory. Two such events that arrive at the
// same moment, with no group open yet, may open a group each.
//
// A joined event counts toward the group's event count, moves the end of
// its window forward, goes to the investigation's inbox, and refreshes what
// the outputs show, so the message has the new count.
//
// Every open group has a close_group job for the end of its window, which
// the Closer runs: when no event has moved the window on and no watcher
// runs, the group closes as window_ended, and the next event with its
// fingerprint opens a new group. A group whose window ended before its job
// ran is closed the same way when the next event arrives.
//
// A recovery event is not routed. Recover links it to the group of the
// newest grouped event with its stored fingerprint, which names one alert,
// and writes it to the investigation's inbox as evidence; it never stops
// the investigation. When the newest event of every alert in an open group
// is a recovery, a close_group job closes the group as resolved.
//
// The fingerprint of an alert under a route with fingerprint labels is made
// from the intake, the alert name and the values of those labels, with a
// missing label counted as empty. Without such labels, for an event that is
// not an alert, or for an alert that has none of the labels, it is the
// fingerprint the intake stored, which names one alert.
package grouper

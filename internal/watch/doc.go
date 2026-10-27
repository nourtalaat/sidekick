// Package watch runs watchers: periodic, read-only checks started for an
// event group that tell when a service is back up after an alert.
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
//
// Start stores a watcher, with its settings resolved from every level of
// defaults, and enqueues its first watch_tick job to run at once. The
// counts against watch.maxPerGroup and watch.maxTotal are taken under a
// transaction-level advisory lock, so they hold across replicas. Each
// watcher has one watch_tick job at a time, with the unique key
// watch:<id>: the Ticker runs one check, stores it, and either ends the
// watcher or enqueues the next tick for one interval later, releasing the
// job's unique key in the same transaction. A watcher ends as recovered
// after passesToRecover passes in a row, as timed_out at its deadline, or
// as stopped when its group resolves or the config no longer allows its
// check, and then enqueues close_group, so a group kept open for its
// watchers closes when the last one ends.
package watch

// Package gate runs the benchmark gate on a proposed strategy change: the
// scenarios that cover the strategy run with its current version and with the
// proposal, and the proposal passes only when it is not clearly worse.
//
// The gate runs inside the server, in a schema of its own so its
// investigations never mix with real ones. It keeps each finished run, so a
// gate that is interrupted carries on from there and does not pay for them
// again.
package gate

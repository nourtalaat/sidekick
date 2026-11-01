// Package proposals drafts changes to strategies from wrong feedback, runs
// them through the benchmark gate, and lets an admin apply the ones that
// passed.
//
// A strategy version with enough wrong feedback in a short time gets one
// proposal. A job asks the summarize model for a changed strategy, and a
// second job runs the scenarios that cover the strategy with the current
// version and with the proposal. The proposal passes only when it is not
// clearly worse. Only a passed proposal can become a strategy version.
package proposals

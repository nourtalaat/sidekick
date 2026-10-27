// Package bench runs benchmark scenarios: `sidekick bench run`.
//
// A scenario file holds an event payload, mock sources that answer the
// investigation's tool calls from fixtures, and the outcome a good
// investigation reaches. Each run of a scenario takes the path an alert
// takes in `sidekick serve`: the payload goes through the real intake
// adapter and is stored, the router opens a group and an investigation,
// and the investigator runs it with the scenario's sources and limits, all
// in this process and in Postgres.
package bench

// Package prometheus is the native source for Prometheus and the servers
// that share its HTTP API, such as Thanos, Mimir, Cortex and
// VictoriaMetrics. It registers itself as the source type "prometheus".
//
// A source reads one server through its HTTP API with a bearer token,
// basic auth or a client certificate from Secrets, read again for each
// request or connection so a rotated secret needs no restart. Every PromQL
// query is parsed with the Prometheus parser before it is sent, so one
// that does not parse is a tool error and never reaches the server.
//
// Every tool only reads. Range queries pick their own step, so a series
// keeps about 250 points, and every result is bounded and says when it was
// cut. Results link to the Prometheus web UI, or to Grafana Explore when a
// Grafana is set in the config.
package prometheus

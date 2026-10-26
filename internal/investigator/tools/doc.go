// Package tools runs source tool calls for investigations.
//
// Expose decides which of a source's tools the model may see, the same way
// for every source kind. A Guard, built once for all sources, gives every
// investigation a Run with a fixed copy of the tools from when it started,
// and runs each call with failure isolation (SRC-10): a timeout, a
// per-source concurrency limit, a circuit breaker, panic recovery, and a
// JSON Schema check of the model's input. Results are redacted and wrapped
// as untrusted data before the model sees them, failures come back as tool
// errors the model can react to, and Run.Checked records what each source
// returned (VR-6). Guard.Do gives other guarded work, such as the HTTP
// probes of watchers, the same timeout, concurrency limit, circuit breaker
// and panic recovery, keyed by target.
package tools

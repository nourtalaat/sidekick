// Package mock is the benchmark's mock action. It registers itself as the
// action type "mock", which only benchmark scenarios use.
//
// A mock action shows the model the description and parameter schema of a
// spec snapshot file, so the model sees what a real deployment of the
// action shows it. It checks the parameters of a proposal against that
// schema, as a real action does, and never runs anything: Execute always
// fails.
package mock

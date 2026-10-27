// Package mock is the benchmark's mock source. It registers itself as the
// source type "mock".
//
// A mock source lists the tools of a snapshot file, so the model sees the
// same names, descriptions and schemas as with the real source, and answers
// each call from fixtures instead of a real system. The fixtures of a tool
// are tried in file order and the first one whose matchers all hold
// answers: with its one response, or with its responses in order, the last
// one repeating. A call no fixture matches gets the source's default: a
// result with no data, or a tool error. An unavailable source fails every
// call, as a source that is down does.
package mock

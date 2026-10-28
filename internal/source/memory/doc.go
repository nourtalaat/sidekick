// Package memory is the memory source: Sidekick's own memory of the
// investigations people confirmed, and the notes people wrote about
// services. It registers itself as the source type "memory".
//
// The source reads Sidekick's database and only reads. Its search tool
// lets the model look for past confirmed investigations on its own, beyond
// the examples the prompt shows, and its service_notes tool reads the
// notes about one service. Disabled entries never match, and investigations
// nobody confirmed are never in memory.
package memory

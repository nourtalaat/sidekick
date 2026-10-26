// Package mcp is the source for Model Context Protocol servers (SRC-1). It
// registers itself as the source type "mcp".
//
// A source talks to one server over streamable HTTP, legacy SSE or stdio,
// with the official go-sdk. It connects in the background and reconnects
// with backoff, so a server that is down never blocks start-up. It lists
// the server's tools when it connects, when the server says the list
// changed, and every ten minutes, and it marks tools that the server's
// annotations say can write as not read-only. Filtering and renaming are
// left to the guard in internal/investigator/tools, like for every source.
package mcp

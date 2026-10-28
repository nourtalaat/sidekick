// Package api holds the OpenAPI description of the Sidekick JSON API,
// api/openapi.yaml. The server code in internal/api/apigen is generated
// from it, and internal/api reads it at start-up for the role of each
// operation.
package api

import _ "embed"

// Spec is the content of openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte

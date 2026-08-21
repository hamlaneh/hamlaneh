// Package api holds the HTTP contract types and server interface generated
// from the OpenAPI specification (docs/api/openapi.yaml — the source of
// truth; never edit api.gen.go by hand).
//
// Regenerate with `go generate ./...` from server/. CI fails when the
// committed generated code drifts from the specification.
package api

//go:generate go tool oapi-codegen -config ../../oapi-codegen.yaml ../../../docs/api/openapi.yaml

// Package api holds the product API contract, OpenAPI 3.1.
//
// openapi.yaml is the root of the source: it names every path and component,
// and each lives in a file per domain under paths/ and components/. `make api`
// bundles them with Redocly CLI into openapi.bundle.yaml, the one document
// that the Go contract tests load and the Web client's types are generated
// from; `make check` fails when the committed bundle is out of date.
package api

import _ "embed"

// Bundle is openapi.bundle.yaml: the whole contract in one document whose
// references all point into itself.
//
//go:embed openapi.bundle.yaml
var Bundle []byte

// Package openapitest gives contract tests the product API contract. They
// load the bundle that the Web client's types come from (api.Bundle), so a
// server that passes them answers what the Web client compiles against. The
// split source would need kin-openapi to resolve references across files the
// way Redocly CLI does; the bundle has none, and `make check` keeps it equal
// to the source.
package openapitest

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	legacyrouter "github.com/getkin/kin-openapi/routers/legacy"
	"github.com/zhongwater123/A-NAS/api"
)

// Document returns the contract, validated.
func Document(t testing.TB) *openapi3.T {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromData(api.Bundle)
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
	return document
}

// Router returns a router that finds the contract's operation for a request.
func Router(t testing.TB) routers.Router {
	t.Helper()
	router, err := legacyrouter.NewRouter(Document(t))
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}
	return router
}

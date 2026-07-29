// Package api holds scry's OpenAPI contract and its committed ogen-generated
// server code. hand-written handlers live in internal/server.
package api

//go:generate go run github.com/ogen-go/ogen/cmd/ogen@v1.23.0 --config ogen.yml --target . --clean specs/scry.yml

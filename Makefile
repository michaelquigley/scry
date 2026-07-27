.PHONY: generate frontend build test clean push
.DEFAULT_GOAL := build

GOBIN ?= $(shell go env GOPATH)/bin

# generate the committed API contract once stage 6 supplies its OpenAPI source.
generate:
	go generate ./...
	@if [ -f internal/api/specs/scry.yml ]; then npm --prefix ui install && npm --prefix ui run gen:api; fi

# build the embedded single-page UI into ui/dist.
frontend:
	npm --prefix ui install
	npm --prefix ui run build

# build the binary. depends on frontend so go:embed all:dist always has content.
build: frontend
	go install ./...

# frontend is a prerequisite because the default Go build embeds ui/dist.
test: frontend
	go test ./... -count=1
	go vet ./...

clean:
	go clean
	rm -f $(GOBIN)/scry scry
	rm -rf ui/dist ui/node_modules

push:
	push vendor $(GOBIN)/scry scry

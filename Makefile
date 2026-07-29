.PHONY: generate frontend build test clean push
.DEFAULT_GOAL := build

GOBIN ?= $(shell go env GOPATH)/bin

# generate both sides of the committed API contract: the ogen server (Go) and
# the dashboard's TypeScript client types.
generate:
	go generate ./...
	npm --prefix ui install
	npm --prefix ui run gen:api

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

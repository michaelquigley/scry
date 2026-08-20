.DEFAULT_GOAL := build

BINARY := scry
GOBIN ?= $(shell go env GOPATH)/bin

.PHONY: generate frontend frontend-test build headless test clean push guard-gobin

guard-gobin:
	@test -n "$(strip $(GOBIN))" || { echo "the 'GOBIN' path is empty" >&2; exit 1; }
	@test "$(abspath $(GOBIN))" != "/" || { echo "the 'GOBIN' path resolves to '/'" >&2; exit 1; }

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

# run the dashboard's ordinary hermetic gate.
frontend-test: frontend
	npm --prefix ui test

# build the binary. depends on frontend so go:embed all:dist always has content.
build: guard-gobin frontend
	GOBIN="$(GOBIN)" go install ./...

# install a headless binary without requiring the embedded dashboard.
headless: guard-gobin
	GOBIN="$(GOBIN)" go install -tags no_ui ./...

# the full gate includes the dashboard because the shipped Go build embeds it.
test: frontend-test
	go test ./... -count=1
	go vet ./...

clean: guard-gobin
	go clean ./...
	rm -f -- "$(GOBIN)"/* "$(BINARY)"
	rm -rf -- ui/dist ui/node_modules

push: build
	push vendor "$(GOBIN)/$(BINARY)" "$(BINARY)"

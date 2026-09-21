.DEFAULT_GOAL := build
GOBIN ?= $(shell go env GOPATH)/bin

ifeq ($(filter-out /,$(abspath $(GOBIN))),)
$(error GOBIN is '$(GOBIN)'; it must name a real directory)
endif

.PHONY: build test clean frontend frontend-test headless generate push

# build the embedded single-page UI into ui/dist.
frontend:
	npm --prefix ui install
	npm --prefix ui run build

# run the dashboard's ordinary hermetic gate.
frontend-test: frontend
	npm --prefix ui test

# depends on frontend so go:embed always has content.
build: frontend
	go install ./...

# install a headless binary without requiring the embedded dashboard.
headless:
	go install -tags no_ui ./...

# the full gate includes the frontend because the shipped Go build embeds it.
test: frontend-test
	go test ./... -count=1
	go vet ./...

clean:
	go clean ./...
	rm -f "$(GOBIN)"/*
	rm -f scry
	rm -rf ui/dist ui/node_modules

# generate both sides of the committed API contract: the ogen server (Go) and
# the dashboard's TypeScript client types.
generate:
	go generate ./...
	npm --prefix ui install
	npm --prefix ui run gen:api

push: build
	push vendor "$(GOBIN)/scry" scry

SHELL := /bin/bash

BIN     := ack
CMD     := ./cmd/ack
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

build:
	go build -ldflags '$(LDFLAGS)' -o ./bin/$(BIN) $(CMD)

install: build
	cp ./bin/$(BIN) $(shell go env GOPATH)/bin/$(BIN)

# Everything that needs neither credentials nor a cluster.
test: lint unit-test unit-test-race

# The tagged vets matter: plain `go vet ./...` skips the AWS and cluster suites, so a
# broken test file there would otherwise surface only when someone runs it.
lint:
	go build ./...
	go vet ./...
	go vet -tags integration ./...
	go vet -tags e2e ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

unit-test:
	go test ./...

unit-test-race:
	go test -race ./...

# Tests that talk to real AWS, behind a build tag so `make test` cannot reach them.
# Needs credentials and AWS_REGION.
test-integration: test-probe test-filters test-adopt

test-probe:
	go test -tags integration -timeout 15m -v ./internal/tagging/

test-filters:
	go test -tags integration -timeout 20m -v ./test/integration/ -run TestTypeFilters

# CREATES AND DELETES real AWS resources, all free of charge.
test-adopt:
	go test -tags integration -timeout 40m -v ./test/integration/ -run TestAdopt

# Additionally needs a cluster running an ACK controller, which test-infra provisions.
test-e2e:
	go test -tags e2e -timeout 40m -v ./test/integration/

fmt:
	gofmt -l -w .

clean:
	rm -rf ./bin

.PHONY: build install test lint unit-test unit-test-race \
	test-integration test-probe test-filters test-adopt test-e2e fmt clean

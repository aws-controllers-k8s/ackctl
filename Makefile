SHELL := /bin/bash

BIN     := ackctl
CMD     := ./cmd/ackctl
GEN     := ./cmd/ack-catalog-gen

# The catalog is generated from each clone's highest stable release tag, so the clones
# need their tags fetched.
CONTROLLERS ?= $(shell cd .. && pwd)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

build:
	go build -ldflags '$(LDFLAGS)' -o ./bin/$(BIN) $(CMD)

install: build
	cp ./bin/$(BIN) $(shell go env GOPATH)/bin/$(BIN)

# Everything that needs neither credentials nor a cluster.
test: lint unit-test unit-test-race

lint:
	go build ./...
	go vet ./...
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

# Type-checks the AWS and cluster suites, which plain `go vet ./...` skips. Kept out of
# `lint` because it compiles the fixture SDKs: aws-sdk-go-v2/service/ec2 alone peaks over
# 3 GiB, which OOM-kills a unit-test container sized for the CLI.
lint-tagged:
	go vet -tags integration ./...
	go vet -tags e2e ./...

unit-test:
	go test ./...

unit-test-race:
	go test -race ./...

catalog:
	go run $(GEN) -controllers $(CONTROLLERS)

# Runs `catalog` over a saved copy and restores it either way, so staleness is reported
# without touching the tree and the two targets cannot disagree.
verify-catalog:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp metadata/adoption_metadata.json metadata/provenance.json "$$tmp/"; \
	$(MAKE) --no-print-directory catalog >/dev/null; \
	status=0; \
	for f in adoption_metadata.json provenance.json; do \
		if ! diff -q "$$tmp/$$f" "metadata/$$f" >/dev/null; then \
			echo "metadata/$$f is stale: run 'make catalog' and commit the result"; \
			status=1; \
		fi; \
		cp "$$tmp/$$f" "metadata/$$f"; \
	done; \
	exit $$status

# Tests that talk to real AWS, behind a build tag so `make test` cannot reach them.
# Needs credentials and AWS_REGION.
test-integration: lint-tagged test-probe test-filters test-adopt test-kinds

test-probe:
	go test -tags integration -timeout 15m -v ./internal/tagging/

test-filters:
	go test -tags integration -timeout 20m -v ./test/integration/ -run TestTypeFilters

# One fixture per adoptable kind that needs no provisioned capacity; the rest carry a
# recorded reason. CREATES AND DELETES real AWS resources.
test-kinds:
	go test -tags integration -timeout 60m -v ./test/integration/ -run 'TestEveryCatalogKindHasAnEntry|TestAdoptEveryKind'

# CREATES AND DELETES real AWS resources in your account. The fixtures provision no
# capacity, but you are responsible for any charges they incur.
test-adopt:
	go test -tags integration -timeout 40m -v ./test/integration/ \
		-run 'TestAdoptIsDeterministic|TestAdoptExplainsAnEmptyResult'

# Additionally needs a cluster running an ACK controller, which test-infra provisions.
test-e2e:
	go test -tags e2e -timeout 40m -v ./test/e2e/

fmt:
	gofmt -l -w .

clean:
	rm -rf ./bin

.PHONY: build install test lint lint-tagged unit-test unit-test-race catalog \
	verify-catalog test-integration test-probe test-filters test-adopt test-kinds \
	test-e2e fmt clean

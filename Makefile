SHELL := /usr/bin/env bash

.PHONY: build test lint vet check clean

build:
	CGO_ENABLED=0 go build -o bin/atc ./cmd/atc

test:
	go test -race ./...

# gofmt has no module awareness, so scope it to the root module's packages;
# this keeps experiments/ and repos/ out without a hand-maintained list.
lint:
	@set -euo pipefail; \
	unformatted="$$(go list -f '{{.Dir}}' ./... | while IFS= read -r dir; do gofmt -l "$$dir" || exit 1; done)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required on:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	go vet ./...

check: build lint vet test

clean:
	rm -rf bin

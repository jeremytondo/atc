.PHONY: all build test lint vet check clean

all: build

build:
	CGO_ENABLED=0 go build -o bin/atc ./cmd/atc

test:
	go test -race ./...

# gofmt has no module awareness, so scope it to the root module's packages;
# this keeps experiments/ and repos/ out without a hand-maintained list.
lint:
	@unformatted="$$(gofmt -l $$(go list -f '{{.Dir}}' ./...))"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt required on:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	go vet ./...

check: lint vet test

clean:
	rm -rf bin

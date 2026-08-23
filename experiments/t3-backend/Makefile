.PHONY: build check test

build:
	go build -o bin/atc-t3 ./cmd/atc-t3

check: test
	go vet ./...

test:
	go test ./...

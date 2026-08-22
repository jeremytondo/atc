.PHONY: build test check

build:
	go build -o bin/atc-acp ./cmd/atc-acp

test:
	go test ./...

check:
	gofmt -w $$(find . -name '*.go' -not -path './bin/*')
	go vet ./...
	go test -race ./...

.PHONY: build test check demo

build:
	mkdir -p bin
	go build -o bin/atc-orchestration ./cmd/atc-orchestration

test:
	go test ./...

check:
	gofmt -w $$(find . -name '*.go' -not -path './bin/*')
	go vet ./...
	go test -race ./...

demo:
	go run ./cmd/atc-orchestration --demo

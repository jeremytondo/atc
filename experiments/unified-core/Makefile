.PHONY: build test check smoke-zmx smoke-acp

build:
	mkdir -p bin
	go build -o bin/atc-unified ./cmd/atc-unified

test:
	go test ./...

check:
	gofmt -w $$(find . -name '*.go' -not -path './bin/*')
	go vet ./...
	go test -race ./...

smoke-zmx: build
	ATC_UNIFIED_ZMX_SMOKE=1 ATC_UNIFIED_BINARY=$$(pwd)/bin/atc-unified go test -count=1 -run TestRealZmx ./internal/adapters/zmx

smoke-acp:
	ATC_UNIFIED_ACP_SMOKE=1 go test -count=1 -run TestRealACP ./internal/adapters/acp

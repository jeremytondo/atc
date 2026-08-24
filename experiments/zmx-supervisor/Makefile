.PHONY: build test check test-zmx

build:
	go build -o bin/atc-zmx ./cmd/atc-zmx

test:
	go test ./...

check:
	gofmt -w $$(find . -name '*.go' -not -path './bin/*')
	go vet ./...
	go test -race ./...

test-zmx:
	ATC_ZMX_SMOKE=1 go test -count=1 -run TestRealZmxLifecycle ./internal/terminal

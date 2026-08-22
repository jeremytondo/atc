.PHONY: build play attach test check smoke-zmx smoke-acp

build:
	mkdir -p bin
	go build -o bin/atc-unified ./cmd/atc-unified

play:
	bash scripts/play.sh

attach:
	@test -n "$(TERMINAL)" || { echo "usage: make attach TERMINAL=term_..." >&2; exit 2; }
	@runtime_dir="$${ATC_UNIFIED_RUNTIME_DIR:-$(CURDIR)/.state}"; \
		ZMX_DIR="$$runtime_dir/core/zmx" ZMX_LOG_DIR="$$runtime_dir/core/logs" zmx attach "$(TERMINAL)"

test:
	go test ./...

check:
	gofmt -w $$(find . -name '*.go' -not -path './bin/*')
	bash -n scripts/play.sh
	go vet ./...
	go test -race ./...

smoke-zmx: build
	ATC_UNIFIED_ZMX_SMOKE=1 ATC_UNIFIED_BINARY=$$(pwd)/bin/atc-unified go test -count=1 -run TestRealZmx ./internal/adapters/zmx

smoke-acp:
	CODEX_HOME="$$(pwd)/.state/codex-home" \
		CLAUDE_CONFIG_DIR="$$(pwd)/.state/claude-home" \
		ATC_UNIFIED_ACP_SMOKE=1 go test -count=1 -run TestRealACP ./internal/adapters/acp

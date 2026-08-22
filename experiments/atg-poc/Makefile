PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build install test

build:
	mkdir -p bin
	go build -o bin/portal ./cmd/portal

install:
	mkdir -p $(BINDIR)
	go build -o $(BINDIR)/portal ./cmd/portal

test:
	go test ./...

GO ?= go
PREFIX ?= $(HOME)/.local
BINDIR ?= bin
BINARY := $(BINDIR)/agentctl
INSTALL_BINARY := $(DESTDIR)$(PREFIX)/bin/agentctl

.PHONY: build test install

build:
	mkdir -p $(BINDIR)
	$(GO) build -o $(BINARY) ./cmd/agentctl

test:
	$(GO) test ./...

install: build
	mkdir -p $(dir $(INSTALL_BINARY))
	cp $(BINARY) $(INSTALL_BINARY)

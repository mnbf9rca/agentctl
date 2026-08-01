GO ?= go
PREFIX ?= $(HOME)/.local
BINDIR ?= bin
BINARY := $(BINDIR)/agentctl
INSTALL_BINARY := $(DESTDIR)$(PREFIX)/bin/agentctl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf development)
VERSION_PACKAGE := github.com/mnbf9rca/agentctl/internal/buildinfo
LDFLAGS := -X $(VERSION_PACKAGE).Stamp=$(VERSION)

.PHONY: build test install

build:
	mkdir -p $(BINDIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/agentctl

test:
	$(GO) test ./...

install: build
	mkdir -p $(dir $(INSTALL_BINARY))
	cp $(BINARY) $(INSTALL_BINARY)

GO ?= go
GOFMT ?= gofmt
VERSION ?= $(shell cat VERSION)
LDFLAGS := -s -w -X mindmory.local/core/internal/version.Value=$(VERSION)
COMMANDS := mindmoryd-lite mindmoryctl mindmory-mcp-stdio mindmory-eval-lite

.DEFAULT_GOAL := help

.PHONY: help format format-check test test-race evaluate build release verify-release clean

help:
	@printf '%s\n' \
	  'Mindmory lite (native Go; Docker is not required)' \
	  '' \
	  '  make format          Format Go source' \
	  '  make format-check    Verify formatting without modifying files' \
	  '  make test            Run all unit and MCP protocol tests' \
	  '  make test-race       Run the race detector' \
	  '  make evaluate        Run the lexical retrieval corpus' \
	  '  make build           Build native binaries in bin/' \
	  '  make release         Build four platform release archives' \
	  '  make verify-release  Audit archives for unexpected/private files'

format:
	$(GOFMT) -w $$(find cmd internal tests -name '*.go' -type f | sort)

format-check:
	@test -z "$$($(GOFMT) -l $$(find cmd internal tests -name '*.go' -type f | sort))" || \
	  { echo 'Go files need formatting; run make format' >&2; $(GOFMT) -l $$(find cmd internal tests -name '*.go' -type f | sort); exit 1; }

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

evaluate:
	mkdir -p var/evaluation
	$(GO) run ./cmd/mindmory-eval-lite -corpus tests/corpus/lite-eval-v2.json -output var/evaluation/lexical.json -model none

build:
	mkdir -p bin
	@for command in $(COMMANDS); do \
	  echo "==> building $$command"; \
	  CGO_ENABLED=0 $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o "bin/$$command" "./cmd/$$command"; \
	done

release: test build
	VERSION='$(VERSION)' sh scripts/package-release.sh
	sh scripts/verify-release.sh packaging/dist

verify-release:
	sh scripts/verify-release.sh packaging/dist

clean:
	@echo 'Remove bin/ and packaging/dist/ manually if you no longer need generated artifacts.'

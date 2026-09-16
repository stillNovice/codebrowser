.PHONY: all build test vet fmt check dist clean help

VERSION ?= $(shell cat VERSION 2>/dev/null | tr -d ' \r\n')
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
VERSION_STAMP := $(VERSION)+$(GIT_SHA)

LDFLAGS := -s -w -X main.BuildVersion=$(VERSION_STAMP)

all: build

help:
	@echo "codebrowse make targets:"
	@echo "  make build      - build ./codebrowse for the current platform (stamps VERSION+git sha)"
	@echo "  make test       - run the full go test suite"
	@echo "  make vet        - gofmt check + go vet"
	@echo "  make check      - vet + test + JS syntax check (pre-push gate)"
	@echo "  make dist       - cross-compile release binaries into dist/ (version-stamped)"
	@echo "  make clean      - remove build artifacts"

build:
	@echo "Building codebrowse $(VERSION_STAMP)..."
	go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o codebrowse .
	@echo "Built ./codebrowse ($$(du -h codebrowse | cut -f1))"

test:
	go test ./...

vet:
	@echo "gofmt:"; @gofmt -l . | tee /dev/stderr | grep -q . && echo "  ^ run: gofmt -w ." && false || true
	go vet ./...

check: vet test
	@node --check internal/server/static/app.js && echo "app.js OK"

dist:
	@mkdir -p dist
	@for t in amd64 arm64; do \
		echo "==> darwin/$$t"; \
		GOOS=darwin GOARCH=$$t go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/codebrowse-darwin-$$t . || exit 1; \
		echo "==> linux/$$t"; \
		GOOS=linux GOARCH=$$t go build -buildvcs=false -trimpath -ldflags="$(LDFLAGS)" -o dist/codebrowse-linux-$$t . || exit 1; \
	done
	@echo "dist/ ready: $$(ls dist | tr '\n' ' ')"

clean:
	rm -f codebrowse
	rm -rf dist/

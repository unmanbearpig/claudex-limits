VERSION ?= dev
GO ?= go
DIST_DIR ?= dist
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test vet race release clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o codex-limits .

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

release:
	set -eu; \
	output="$(DIST_DIR)/$(VERSION)"; \
	mkdir -p "$$output"; \
	set --; \
	for target in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do \
		os=$${target%-*}; arch=$${target#*-}; \
		mkdir -p "$$output/$$target"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$$output/$$target/codex-limits" .; \
		archive="codex-limits-$(VERSION)-$$target.tar.gz"; \
		tar -czf "$$output/$$archive" -C "$$output/$$target" codex-limits -C "$(CURDIR)" README.md LICENSE docs/demo.svg docs/claude-quotas.md; \
		rm -f "$$output/$$target/codex-limits"; \
		rmdir "$$output/$$target"; \
		set -- "$$@" "$$archive"; \
	done; \
	if command -v sha256sum >/dev/null 2>&1; then (cd "$$output" && sha256sum "$$@" > SHA256SUMS); \
	elif command -v shasum >/dev/null 2>&1; then (cd "$$output" && shasum -a 256 "$$@" > SHA256SUMS); \
	else echo 'no SHA-256 utility found' >&2; exit 1; fi

clean:
	rm -rf dist

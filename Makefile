VERSION ?= dev
GO ?= go
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
	mkdir -p dist; \
	for target in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do \
		os=$${target%-*}; arch=$${target#*-}; suffix=; [ "$$os" = windows ] && suffix=.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "dist/codex-limits-$$target$$suffix" .; \
		archive="dist/codex-limits-$(VERSION)-$$target.tar.gz"; \
		tar -czf "$$archive" -C dist "codex-limits-$$target$$suffix" -C .. README.md; \
		rm -f "dist/codex-limits-$$target$$suffix"; \
	done; \
	if command -v sha256sum >/dev/null 2>&1; then (cd dist && sha256sum codex-limits-$(VERSION)-*.tar.gz > SHA256SUMS); \
	elif command -v shasum >/dev/null 2>&1; then (cd dist && shasum -a 256 codex-limits-$(VERSION)-*.tar.gz > SHA256SUMS); \
	else echo 'no SHA-256 utility found' >&2; exit 1; fi

clean:
	rm -rf dist

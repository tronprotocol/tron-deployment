BINARY     := trond
MODULE     := github.com/tronprotocol/tron-deployment
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS    := -s -w -X $(MODULE)/cmd.version=$(VERSION) -X $(MODULE)/cmd.commit=$(COMMIT) -X $(MODULE)/cmd.buildTime=$(BUILD_TIME)

# --- Project-local Go toolchain --------------------------------------
#
# We install Go into .go-toolchain/<ver>/ and route every Make target
# through it via $(GO). Module cache + tool binaries land under .gopath/
# in the same project so a fresh `make clean-all` removes every byte
# this repo ever downloaded.
#
# Why: avoids the "which Go does the user have" question entirely. A
# fresh clone followed by `make build` produces an identical binary
# regardless of what's installed on the host, and the build never
# pollutes the user's $HOME/go cache.
#
# The user can still set USE_SYSTEM_GO=1 to fall back to whatever `go`
# resolves on PATH (useful in CI runners that already pinned Go via
# actions/setup-go and want to skip the download step).

GO_VERSION ?= 1.25.13

ifeq ($(USE_SYSTEM_GO),1)
GO         := go
GO_BOOTSTRAP :=
else
GO         := $(CURDIR)/.go-toolchain/$(GO_VERSION)/bin/go
GO_BOOTSTRAP := bootstrap-go
export GOROOT := $(CURDIR)/.go-toolchain/$(GO_VERSION)
export GOPATH := $(CURDIR)/.gopath
# GOBIN must override the user's shell-exported GOBIN. Without this,
# `go install` writes binaries into the user's $HOME/go/bin (where the
# user has GOBIN pointing) and our recipes can't find them under
# $(GOPATH)/bin. Forcing it here keeps the install fully scoped.
export GOBIN  := $(GOPATH)/bin
export PATH   := $(GOROOT)/bin:$(GOBIN):$(PATH)
endif

GOFLAGS    ?=

.PHONY: build test lint e2e build-all clean clean-all fmt vet tidy sync-templates sync-schemas sync-knowledge snapshot-schema-baseline update-render-golden docs man cover vuln bootstrap-go build-replay install-replay build-txgen install-txgen build-txgen-falcon install-txgen-falcon

## bootstrap-go: Download + verify the project-local Go toolchain
##               (idempotent; safe to re-run; no-op if already current)
bootstrap-go:
	@GO_VERSION=$(GO_VERSION) $(CURDIR)/scripts/bootstrap-go.sh

## build: Build the trond binary for the current platform
build: $(GO_BOOTSTRAP)
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

## test: Run unit tests
test: $(GO_BOOTSTRAP)
	$(GO) test ./... -race -count=1

## lint: Run golangci-lint (compiled with the project Go toolchain)
##       so the linter and the project agree on the language version.
##       v1.64.8 is the last v1-line release; v2 line requires a config
##       schema migration. Bump only when the project moves to v2.
lint: $(GO_BOOTSTRAP)
	@$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8
	$(GOPATH)/bin/golangci-lint run --timeout=5m ./...

## e2e: Run end-to-end tests (requires Docker)
e2e: $(GO_BOOTSTRAP)
	$(GO) test ./... -tags=e2e -race -count=1 -timeout 10m

## build-all: Cross-compile for all supported platforms
build-all: $(GO_BOOTSTRAP)
	GOOS=linux  GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .
	GOOS=linux  GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-darwin-arm64 .

## clean: Remove build artifacts (keeps the toolchain so re-builds are fast)
clean:
	rm -rf bin/

## clean-all: clean + remove the project-local Go toolchain and gopath.
##            Use this when bumping GO_VERSION or to reclaim disk.
##            Module cache entries are 0444 by design; chmod first.
clean-all: clean
	@if [ -d .gopath ]; then chmod -R u+w .gopath; fi
	rm -rf .go-toolchain/ .gopath/

## fmt: Format Go source files
fmt: $(GO_BOOTSTRAP)
	$(GO) fmt ./...

## vet: Run go vet
vet: $(GO_BOOTSTRAP)
	$(GO) vet ./...

## tidy: Tidy go.mod
tidy: $(GO_BOOTSTRAP)
	$(GO) mod tidy

## docs: Generate per-command markdown reference (dist/docs/)
docs: $(GO_BOOTSTRAP)
	@mkdir -p dist/docs
	$(GO) run ./cmd/gendoc md dist/docs

## man: Generate man(1) pages (dist/man/)
man: $(GO_BOOTSTRAP)
	@mkdir -p dist/man
	$(GO) run ./cmd/gendoc man dist/man

## cover: Run tests with coverage and print per-function summary
cover: $(GO_BOOTSTRAP)
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail

## vuln: Run govulncheck against the module
vuln: $(GO_BOOTSTRAP)
	@$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GOPATH)/bin/govulncheck ./...

## sync-templates: Refresh mainnet + nile config templates from upstream
##                 Source-of-truth URLs:
##                   mainnet: tronprotocol/java-tron develop branch
##                   nile:    tron-nile-testnet/nile-testnet master branch
##                 The private_net_config.conf is maintained in-repo and is
##                 NOT refreshed by this target.
MAINNET_URL := https://raw.githubusercontent.com/tronprotocol/java-tron/develop/framework/src/main/resources/config.conf
NILE_URL    := https://raw.githubusercontent.com/tron-nile-testnet/nile-testnet/master/framework/src/main/resources/config-nile.conf

sync-templates:
	@echo "fetching mainnet template..."
	curl -fsSL $(MAINNET_URL) -o main_net_config.conf
	cp main_net_config.conf internal/render/templates/main_net_config.conf
	@echo "fetching nile template..."
	curl -fsSL $(NILE_URL) -o test_net_config.conf
	cp test_net_config.conf internal/render/templates/test_net_config.conf
	@echo "templates refreshed. Re-run 'make build test' to confirm."

## sync-schemas: Mirror schemas/output/ into internal/schema/files/ so
##               the embedded copies bundled into the binary match the
##               source tree GitHub renders. The unit test
##               TestEmbeddedSchemasMatchSourceTree fails on drift; run
##               this target after editing any schema under schemas/.
sync-schemas:
	@echo "syncing schemas/output/ → internal/schema/files/"
	@cp schemas/output/*.schema.json internal/schema/files/
	@echo "done. Re-run 'make test' to confirm."

## sync-knowledge: Mirror knowledge/*.md into internal/knowledge/files/
##                 so `trond knowledge <topic>` returns the same content
##                 GitHub renders. Run after editing any operator doc
##                 under knowledge/.
sync-knowledge:
	@echo "syncing knowledge/*.md → internal/knowledge/files/"
	@cp knowledge/*.md internal/knowledge/files/
	@echo "done. Rebuild the binary to pick up the new embed."

## snapshot-schema-baseline: Refresh internal/schema/version_baseline.json
##                           after intentional schema changes. The test
##                           TestSchemaVersionShape compares the current
##                           embedded schemas against this baseline; run
##                           this target as the second step of any
##                           schema edit (the first being SchemaVersion).
snapshot-schema-baseline: $(GO_BOOTSTRAP)
	TROND_SCHEMA_UPDATE_BASELINE=1 $(GO) test -run TestSchemaVersionShape ./internal/schema/
	@echo "baseline refreshed. Commit version_baseline.json with the SchemaVersion bump."

## update-render-golden: Refresh internal/render/testdata/golden/*.{conf,compose.yaml}
##                       after intentional template changes. TestRenderHOCON_Golden
##                       compares current render output against these files; run
##                       this target to accept the new output as the new baseline.
update-render-golden: $(GO_BOOTSTRAP)
	TROND_UPDATE_GOLDEN=1 $(GO) test -run TestRenderHOCON_Golden ./internal/render/
	@echo "render golden files refreshed under internal/render/testdata/golden/."

## build-replay: Build the tools/replay binary into bin/replay.
##               replay is a standalone Go binary (no java-tron source
##               dependency) that streams mainnet historical transactions
##               from TronGrid to a private chain over HTTP. See
##               tools/replay/README.md and tools/replay/IMPROVEMENTS.md.
build-replay: $(GO_BOOTSTRAP)
	@mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/replay ./tools/replay
	@echo "✓ bin/replay built"

## install-replay: Build + copy bin/replay into $(GOBIN).
install-replay: build-replay
	cp bin/replay $(GOBIN)/replay
	@echo "✓ replay installed at $(GOBIN)/replay"

## build-txgen: Build the tools/txgen binary into bin/txgen.
##              txgen is a standalone Go binary (no java-tron source
##              dependency) that generates + broadcasts synthetic TRON
##              transactions (TRX / TRC10 / TRC20) for stress testing.
##              See tools/txgen/README.md.
build-txgen: $(GO_BOOTSTRAP)
	@mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/txgen ./tools/txgen
	@echo "✓ bin/txgen built"

## install-txgen: Build + copy bin/txgen into $(GOBIN).
install-txgen: build-txgen
	cp bin/txgen $(GOBIN)/txgen
	@echo "✓ txgen installed at $(GOBIN)/txgen"

# --- liboqs pkg-config helpers (used by build-txgen-falcon) ------------------
# Falls back to common homebrew paths when pkg-config is not found or liboqs
# is not registered. Override by setting LIBOQS_CFLAGS / LIBOQS_LDFLAGS in
# the environment before calling make.
LIBOQS_CFLAGS  ?= $(shell pkg-config --cflags liboqs 2>/dev/null || echo -I/usr/local/include -I/opt/homebrew/include)
LIBOQS_LDFLAGS ?= $(shell pkg-config --libs   liboqs 2>/dev/null || echo -L/usr/local/lib -L/opt/homebrew/lib -loqs)
OPENSSL_LDFLAGS ?= $(shell pkg-config --libs  openssl 2>/dev/null || echo -L/usr/local/opt/openssl@3/lib -L/opt/homebrew/opt/openssl@3/lib -lcrypto)

## build-txgen-falcon: Build bin/txgen with Falcon-512 (FN_DSA_512) support.
##              Requires liboqs ≥ 0.10 installed (brew install liboqs on macOS).
##              Uses CGO + liboqs C library; produces a larger binary that can
##              sign FN_DSA_512 PQ transactions in addition to ML_DSA_44.
##              Skipped with a warning (exit 0) when liboqs is not installed,
##              so CI runners without liboqs do not fail.
build-txgen-falcon: $(GO_BOOTSTRAP)
	@if ! (pkg-config --exists liboqs 2>/dev/null || \
	       [ -f /usr/local/include/oqs/oqs.h ] || \
	       [ -f /opt/homebrew/include/oqs/oqs.h ]); then \
		echo "⚠  liboqs not found — skipping build-txgen-falcon"; \
		echo "   Install with: brew install liboqs  (macOS)"; \
		echo "   or build from source: https://github.com/open-quantum-safe/liboqs"; \
		exit 0; \
	fi; \
	mkdir -p bin; \
	echo "building bin/txgen with Falcon-512 (liboqs)..."; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="$(LIBOQS_CFLAGS)" \
	CGO_LDFLAGS="$(LIBOQS_LDFLAGS) $(OPENSSL_LDFLAGS)" \
	$(GO) build -tags falcon -ldflags "$(LDFLAGS)" -o bin/txgen ./tools/txgen && \
	echo "✓ bin/txgen (with Falcon-512 via liboqs) built"

## install-txgen-falcon: Build + copy Falcon-enabled txgen into $(GOBIN).
install-txgen-falcon: build-txgen-falcon
	cp bin/txgen $(GOBIN)/txgen
	@echo "✓ txgen (with Falcon-512) installed at $(GOBIN)/txgen"

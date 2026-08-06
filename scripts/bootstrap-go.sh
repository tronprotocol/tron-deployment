#!/usr/bin/env bash
#
# Download a known-version Go toolchain into the project's own
# .go-toolchain/<version>/ directory and verify its SHA256 against the
# upstream-published hash. Idempotent — re-runs are no-ops once the
# tree exists.
#
# Used by Makefile so `make build` works on a fresh clone without the
# user having to install Go (or worry about which Go they have).
# Mirrors the spirit of tron-docker's build_trond.sh while keeping the
# Go install scoped to this project alone — nothing leaks into ~/go,
# /usr/local/go, or the user's PATH.
#
# Module cache + binaries land under .gopath/ in the same project so a
# fresh `make clean-all` removes every byte downloaded for this repo.

set -euo pipefail

# Resolve to the project root regardless of where the script is invoked.
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
PROJECT_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
cd "$PROJECT_ROOT"

GO_VERSION=${GO_VERSION:-1.25.9}
GO_DIR=".go-toolchain/${GO_VERSION}"

# Per-platform tarball + sha256. When bumping GO_VERSION, refresh the
# four hashes from `curl -sL https://go.dev/dl/?mode=json` — the official
# "Go Releases" page exposes them as the canonical source. Don't read
# them from arbitrary mirrors; that's exactly the supply-chain risk
# we're guarding against.
case "$(uname -s)" in
    Linux)  os="linux"  ;;
    Darwin) os="darwin" ;;
    *)
        echo "bootstrap-go: unsupported OS $(uname -s)" >&2
        exit 1
        ;;
esac
case "$(uname -m)" in
    x86_64|amd64)   arch="amd64" ;;
    arm64|aarch64)  arch="arm64" ;;
    *)
        echo "bootstrap-go: unsupported arch $(uname -m)" >&2
        exit 1
        ;;
esac

archive="go${GO_VERSION}.${os}-${arch}.tar.gz"
url="https://go.dev/dl/${archive}"

# Each line is "<sha256>  <filename>". Pulled from go.dev/dl/?mode=json
# at the time GO_VERSION was set.
case "${os}-${arch}" in
    linux-amd64)  expected_sha="00859d7bd6defe8bf84d9db9e57b9a4467b2887c18cd93ae7460e713db774bc1" ;;
    linux-arm64)  expected_sha="ec342e7389b7f489564ed5463c63b16cf8040023dabc7861256677165a8c0e2b" ;;
    darwin-amd64) expected_sha="92cb78fba4796e218c1accb0ea0a214ef2094c382049a244ad6505505d015fbe" ;;
    darwin-arm64) expected_sha="9528be7329b9770631a6bd09ca2f3a73ed7332bec01d87435e75e92d8f130363" ;;
    *)
        echo "bootstrap-go: no SHA256 recorded for ${os}-${arch}" >&2
        exit 1
        ;;
esac

# Fast path: already extracted, binary works.
#
# GOTOOLCHAIN=local is load-bearing. Without it `go env GOVERSION` reports
# the EFFECTIVE toolchain, and Go 1.21+ auto-upgrades to whatever go.mod's
# `toolchain` line names — so an installed go1.25.9 reported itself as
# go1.25.11, never matched GO_VERSION, and every single make invocation
# deleted the toolchain and re-downloaded 55 MB. The check has to ask what
# is installed here, not what this repo would rather use.
if [ -x "${GO_DIR}/bin/go" ]; then
    actual_version=$(GOTOOLCHAIN=local "${GO_DIR}/bin/go" env GOVERSION 2>/dev/null || echo "")
    if [ "$actual_version" = "go${GO_VERSION}" ]; then
        # Already correct version; nothing to do.
        exit 0
    fi
    echo "bootstrap-go: existing ${GO_DIR} reports ${actual_version}, expected go${GO_VERSION} — refreshing" >&2
    rm -rf "${GO_DIR}"
fi

mkdir -p "${GO_DIR}"
trap 'echo "bootstrap-go: aborting" >&2; rm -rf "${GO_DIR}"' ERR

# Download into a tmp file inside the toolchain dir so a partial
# transfer doesn't survive an interrupted run.
tmp="${GO_DIR}/${archive}.tmp"

echo "bootstrap-go: downloading ${url}" >&2
if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --retry-delay 2 -o "${tmp}" "${url}"
elif command -v wget >/dev/null 2>&1; then
    wget -O "${tmp}" "${url}"
else
    echo "bootstrap-go: need curl or wget on PATH" >&2
    exit 1
fi

# Compute the actual SHA and compare. Two tools because Linux distros
# ship sha256sum and macOS ships shasum -a 256 — neither is universal.
if command -v sha256sum >/dev/null 2>&1; then
    actual_sha=$(sha256sum "${tmp}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
    actual_sha=$(shasum -a 256 "${tmp}" | awk '{print $1}')
else
    echo "bootstrap-go: no sha256sum or shasum on PATH" >&2
    exit 1
fi

if [ "${actual_sha}" != "${expected_sha}" ]; then
    echo "bootstrap-go: SHA256 mismatch for ${archive}" >&2
    echo "  expected: ${expected_sha}" >&2
    echo "  actual:   ${actual_sha}" >&2
    rm -f "${tmp}"
    exit 1
fi

# Extract with --strip-components=1 so go's own "go/bin/go" lands at
# .go-toolchain/<ver>/bin/go (we already created the version dir).
tar -xzf "${tmp}" -C "${GO_DIR}" --strip-components=1
rm -f "${tmp}"

trap - ERR

# Sanity-check the resulting binary so a corrupted-but-correct-sha
# (extremely unlikely but possible if upstream gets compromised post-
# publication) doesn't masquerade as a working install.
if ! "${GO_DIR}/bin/go" version >/dev/null 2>&1; then
    echo "bootstrap-go: extracted go binary refuses to run; check ${GO_DIR}" >&2
    rm -rf "${GO_DIR}"
    exit 1
fi

echo "bootstrap-go: ready — $("${GO_DIR}/bin/go" version)" >&2

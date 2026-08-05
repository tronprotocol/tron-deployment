#!/usr/bin/env bash
# =============================================================================
# scripts/gen-tron-protos.sh — regenerate internal/tronproto/pb/*.pb.go
#
# Driven by `go generate ./internal/tronproto/...`. Idempotent. Assumes
# protoc + protoc-gen-go are on PATH (see proto/README.md for install).
#
# Strategy: only generate the messages dbfork's mutation engine touches.
# Everything in upstream/ stays available for future extension via additional
# entries in PROTO_FILES below.
# =============================================================================
set -euo pipefail

# --- Toolchain prechecks (fail-loud) --------------------------------
for bin in protoc protoc-gen-go protoc-gen-go-grpc; do
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "error: '$bin' not found on PATH." >&2
        case "$bin" in protoc-gen-go|protoc-gen-go-grpc)
            echo "       Install the pinned version: go install tool" >&2
            echo "       (Reads the version from go.mod's tool directive.)" >&2
            ;;
        esac
        echo "       See internal/tronproto/README.md for full instructions." >&2
        exit 1
    fi
done

cd "$(dirname "$0")/.."   # repo root
UPSTREAM="internal/tronproto/upstream"
# googleapis annotations, imported by api/api.proto. Kept OUTSIDE upstream/
# because upstream/ is a git subtree of java-tron's protos — adding files
# there would conflict on the next `git subtree pull`.
THIRDPARTY="internal/tronproto/thirdparty"
OUT="internal/tronproto/pb"

# --- WARNING: $OUT is wiped + regenerated on every run --------------
# Do NOT hand-edit *.pb.go files; they're machine-generated and will
# be clobbered. Both .pb.go and this dir are committed so `go build`
# doesn't need protoc on every machine — to refresh, edit the upstream
# .proto (via subtree pull, NOT directly) and re-run this script.

# Subset we GENERATE Go bindings for. Add entries here as dbfork's
# surface grows. Files transitively imported by these (but not in
# this list) get their go_package overridden too — see ALL_PROTOS
# below — but no .pb.go is emitted for them.
PROTO_FILES=(
    # api/api.proto carries the Wallet gRPC SERVICE definition. It is the
    # only entry here generated for its service stubs rather than its
    # messages: tools/txgen's gRPC transport needs a real WalletClient, and
    # hand-writing a trimmed copy of the service would silently drift from
    # upstream the first time a signature changed. Pulling it in also
    # generates its message set and the contract protos it imports.
    "api/api.proto"
    "core/Tron.proto"
    "core/Discover.proto"                       # Endpoint, used by Tron.proto
    "core/contract/account_contract.proto"
    "core/contract/asset_issue_contract.proto"
    "core/contract/smart_contract.proto"
    "core/contract/common.proto"
    "core/contract/balance_contract.proto"
    # Pulled in by api/api.proto's service methods — the Wallet service
    # signature references every contract type, so generating the service
    # requires the full import closure, not just dbfork's subset.
    "core/contract/witness_contract.proto"
    "core/contract/proposal_contract.proto"
    "core/contract/storage_contract.proto"
    "core/contract/exchange_contract.proto"
    "core/contract/market_contract.proto"
    "core/contract/shield_contract.proto"
    "core/contract/vote_asset_contract.proto"
    "core/tron/account.proto"
    "core/tron/transaction.proto"
)

# All bindings share this go module prefix so imports resolve.
GO_PACKAGE_PREFIX="github.com/tronprotocol/tron-deployment/internal/tronproto/pb"

# Build M-flags for EVERY .proto file in upstream/. All map to the
# SAME Go package, which mirrors the .proto files' single
# `package protocol;` namespace. Splitting them by directory (e.g.
# core/Tron.proto → pb/core, core/contract/x.proto → pb/core/contract)
# creates Go import cycles because the .proto messages cross those
# boundaries freely (Tron.Account references contract messages and
# vice versa). One Go package = no cycles, matches upstream design.
# Portable read-loop instead of `mapfile -t` (bash 4+ only — macOS
# system bash is 3.2, so contributors invoking `bash scripts/...`
# instead of `./scripts/...` would otherwise hit a confusing
# "mapfile: command not found").
ALL_PROTOS=()
while IFS= read -r line; do
    ALL_PROTOS+=("$line")
done < <(cd "$UPSTREAM" && find . -name "*.proto" -type f | sed 's|^\./||' | sort)
# Fail loud if upstream/ is empty (botched subtree, mid-rebase) —
# without this guard, `${ALL_PROTOS[@]}` under `set -u` bash 3.2
# would die with the confusing "unbound variable".
if [ ${#ALL_PROTOS[@]} -eq 0 ]; then
    echo "error: no .proto files found under $UPSTREAM" >&2
    echo "       Did the git subtree pull at proto/README.md complete?" >&2
    exit 1
fi
GO_OPTS=()
GRPC_OPTS=()
for f in "${ALL_PROTOS[@]}"; do
    # M flag format: <proto-path>=<go-import>;<go-pkg-name>
    # All files share both the import path AND the package name "tronpb"
    # so they compile as a single Go package without name collisions.
    # (Without ;tronpb, protoc-gen-go infers the package name from the
    # .proto's `package protocol.contract;` etc. and they diverge.)
    GO_OPTS+=("--go_opt=M${f}=${GO_PACKAGE_PREFIX};tronpb")
    # protoc-gen-go-grpc needs the identical mapping — it resolves the Go
    # import path of every message a service method references.
    GRPC_OPTS+=("--go-grpc_opt=M${f}=${GO_PACKAGE_PREFIX};tronpb")
done

# Ensure the output dir exists (otherwise protoc errors). Empty it
# first so stale generated files from prior runs don't linger after
# we drop a proto from the subset. The pb/ banner above + the
# `// Code generated ... DO NOT EDIT.` header on every emitted .pb.go
# is the contract: do NOT hand-edit pb/, this wipes them.
echo "wiping $OUT (regenerating — never hand-edit *.pb.go)" >&2
rm -rf "$OUT"
mkdir -p "$OUT"

# protoc emits flat into $OUT because every file maps to the same
# Go package (see ALL_PROTOS loop above). `module=` strips the
# upstream's go_package URL prefix; output files land at
# $OUT/<basename>.pb.go directly. Tron.pb.go, smart_contract.pb.go,
# etc. all in one dir → one Go package → no import cycles.
protoc \
    -I="$UPSTREAM" \
    -I="$THIRDPARTY" \
    "${GO_OPTS[@]}" \
    "${GRPC_OPTS[@]}" \
    --go_out="$OUT" \
    --go_opt=module="$GO_PACKAGE_PREFIX" \
    --go-grpc_out="$OUT" \
    --go-grpc_opt=module="$GO_PACKAGE_PREFIX" \
    --go-grpc_opt=require_unimplemented_servers=false \
    "${PROTO_FILES[@]/#/$UPSTREAM/}"

echo "Generated:"
find "$OUT" -name "*.pb.go" -print | sort

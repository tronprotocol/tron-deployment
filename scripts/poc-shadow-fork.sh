#!/usr/bin/env bash
# End-to-end PoC for shadow-fork testing on a real Nile snapshot.
#
# Workflow:
#
#   1. setup    — download Nile snapshot (~30 min, ~10-20 GB),
#                 generate witness keypair, expand fork.conf +
#                 intent.yaml templates.
#   2. mutate   — apply fork.conf to the snapshot via
#                 `trond shadow-fork mutate`.
#   3. apply    — launch the single-witness shadow-fork node via
#                 `trond apply`.
#   4. observe  — tail the node's logs + poll the JSON-RPC for
#                 block production; pass once 5+ blocks land.
#   5. teardown — `trond remove` the node (optional; preserves the
#                 mutated data so you can re-apply).
#
# Each subcommand is idempotent — running `setup` twice doesn't
# re-download; running `mutate` twice re-applies cleanly because
# the fork.conf mutations are themselves idempotent.
#
# Phase 1 caveats:
#   - Single witness = no finality (chain produces blocks every 3s
#     but the SR confirmation count stays at 1, well below the 19/27
#     threshold). Sufficient for "did the shadow-fork mutation work"
#     proof; not for testing finality-dependent code paths.
#   - Witness key generation requires `tronpy` (`pip install tronpy`)
#     OR the operator providing their own via SHADOW_FORK_WITNESS_KEY
#     + SHADOW_FORK_WITNESS_ADDRESS env vars.
#
# Usage:
#   ./scripts/poc-shadow-fork.sh setup
#   ./scripts/poc-shadow-fork.sh mutate
#   ./scripts/poc-shadow-fork.sh apply
#   ./scripts/poc-shadow-fork.sh observe
#   ./scripts/poc-shadow-fork.sh teardown
#
# Or the all-in-one (sequence the first four):
#   ./scripts/poc-shadow-fork.sh all

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DATA_DIR="${SHADOW_FORK_DATA_DIR:-${REPO_ROOT}/shadow-fork-data}"
FORK_CONF="${REPO_ROOT}/shadow-fork.conf"
INTENT="${REPO_ROOT}/shadow-fork-intent.yaml"
NODE_NAME="shadow-fork-poc"
TROND_BIN="${TROND_BIN:-${REPO_ROOT}/bin/trond}"

# Where the operator stashes the generated keypair so subsequent
# subcommands can resolve it. Kept out of git via .gitignore.
KEY_STASH="${REPO_ROOT}/.shadow-fork-witness.env"

log() { printf '[poc-shadow-fork] %s\n' "$*"; }
err() { printf '[poc-shadow-fork] ERROR: %s\n' "$*" >&2; exit 1; }

ensure_trond() {
  if ! command -v "$TROND_BIN" >/dev/null 2>&1; then
    err "$TROND_BIN not found — run 'make build' first"
  fi
}

# Create the key stash empty and private BEFORE any secret byte is
# written to it. A `chmod 600` applied after the write leaves the
# private key on disk at the umask default (0644 under the usual 022)
# for the whole length of the write, and truncating an existing
# loose-moded stash keeps that loose mode regardless of umask. The
# unlink also means we never write a private key through a symlink (or
# into a FIFO / pre-planted hardlink) sitting at that path.
init_key_stash() {
  if [[ -L "$KEY_STASH" ]]; then
    log "replacing symlink at $KEY_STASH with a private regular file"
  fi
  rm -f "$KEY_STASH" || err "cannot replace $KEY_STASH"
  (umask 077; : > "$KEY_STASH") || err "cannot create $KEY_STASH"
}

generate_witness_key() {
  # Generate a fresh secp256k1 keypair + derive the TRON Base58Check
  # address. We don't have a Go subcommand for this (out of trond's
  # scope), so we shell out to Python's tronpy — well-maintained,
  # widely used, pure-Python install. The script can ALSO read a
  # caller-supplied key from env vars if they have their own.
  if [[ -n "${SHADOW_FORK_WITNESS_KEY:-}" && -n "${SHADOW_FORK_WITNESS_ADDRESS:-}" ]]; then
    log "using caller-supplied SHADOW_FORK_WITNESS_KEY + SHADOW_FORK_WITNESS_ADDRESS"
    init_key_stash
    cat > "$KEY_STASH" <<EOF
export SHADOW_FORK_WITNESS_KEY="$SHADOW_FORK_WITNESS_KEY"
export SHADOW_FORK_WITNESS_ADDRESS="$SHADOW_FORK_WITNESS_ADDRESS"
EOF
    log "stashed at $KEY_STASH (mode 0600)"
    return
  fi
  if [[ -f "$KEY_STASH" ]]; then
    log "witness keypair already generated: $KEY_STASH (delete to regenerate)"
    return
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    err "python3 required to generate witness key — install or set SHADOW_FORK_WITNESS_KEY+ADDRESS"
  fi
  if ! python3 -c 'import tronpy' 2>/dev/null; then
    err "tronpy not installed — run 'pip install tronpy' or set SHADOW_FORK_WITNESS_KEY+ADDRESS"
  fi
  log "generating fresh witness keypair via tronpy"
  init_key_stash
  python3 - <<'PY' > "$KEY_STASH"
from tronpy.keys import PrivateKey
k = PrivateKey.random()
addr = k.public_key.to_base58check_address()
print(f'export SHADOW_FORK_WITNESS_KEY="{k.hex()}"')
print(f'export SHADOW_FORK_WITNESS_ADDRESS="{addr}"')
PY
  # The file holds a fresh secp256k1 private key; init_key_stash above
  # created it 0600 so the key is never on disk world-readable, not
  # even for the length of the write. .gitignore catches the commit
  # path; the mode protects against shared-filesystem leaks +
  # accidental `tar`/`zip` exposure.
  log "stashed at $KEY_STASH (mode 0600)"
}

expand_templates() {
  # shellcheck source=/dev/null
  source "$KEY_STASH"
  local now_ms next_ms
  now_ms=$(($(date +%s) * 1000))
  next_ms=$((now_ms + 60000)) # 1 minute in the future

  sed \
    -e "s|<WITNESS_TRON_ADDRESS>|$SHADOW_FORK_WITNESS_ADDRESS|g" \
    -e "s|<NOW_MS>|$now_ms|g" \
    -e "s|<NEXT_MAINTENANCE_MS>|$next_ms|g" \
    "$REPO_ROOT/examples/shadow-fork/fork.conf.template" > "$FORK_CONF"
  log "wrote $FORK_CONF (witness=$SHADOW_FORK_WITNESS_ADDRESS, latestBlockHeaderTimestamp=$now_ms)"

  sed \
    -e "s|<SHADOW_FORK_DATA_DIR>|$DATA_DIR|g" \
    "$REPO_ROOT/examples/shadow-fork/intent.yaml.template" > "$INTENT"
  log "wrote $INTENT (storage.data=$DATA_DIR/output-directory)"

  # Guard against unsubstituted placeholders — if a future template
  # edit adds <FOO> but the script's sed list isn't updated, the
  # rendered file ships broken. Inline check is cheap.
  for f in "$FORK_CONF" "$INTENT"; do
    if grep -qE '<[A-Z][A-Z0-9_]*>' "$f"; then
      err "$f has unsubstituted placeholders — $(grep -oE '<[A-Z][A-Z0-9_]*>' "$f" | sort -u | tr '\n' ' ')"
    fi
  done
}

cmd_setup() {
  ensure_trond
  mkdir -p "$DATA_DIR"

  if [[ ! -d "$DATA_DIR/output-directory/database" ]]; then
    log "downloading Nile snapshot to $DATA_DIR (this takes ~30 min)"
    "$TROND_BIN" snapshot download --network nile --type lite --to "$DATA_DIR"
  else
    log "snapshot already present at $DATA_DIR/output-directory/database — skipping download"
  fi

  generate_witness_key
  expand_templates

  log "setup done. Next: ./scripts/poc-shadow-fork.sh mutate"
}

cmd_mutate() {
  ensure_trond
  if [[ ! -f "$FORK_CONF" ]]; then
    err "$FORK_CONF not present — run 'setup' first"
  fi
  log "applying fork.conf to $DATA_DIR/output-directory"
  "$TROND_BIN" shadow-fork mutate \
    -d "$DATA_DIR/output-directory" \
    -c "$FORK_CONF" \
    --output json
  log "mutation done. Next: ./scripts/poc-shadow-fork.sh apply"
}

cmd_apply() {
  ensure_trond
  if [[ ! -f "$INTENT" ]]; then
    err "$INTENT not present — run 'setup' first"
  fi
  if [[ ! -f "$KEY_STASH" ]]; then
    err "$KEY_STASH not present — run 'setup' first"
  fi
  # shellcheck source=/dev/null
  source "$KEY_STASH"
  log "launching shadow-fork node from $INTENT"
  # --auto-approve: setup regenerates the latestBlockHeaderTimestamp
  # on each invocation, so the intent hash changes; without
  # --auto-approve, the second run of `apply` would fail with
  # HUMAN_REQUIRED (exit 10). --wait blocks until the container
  # reports healthy so the next phase's observe step doesn't poll
  # an unborn JSON-RPC endpoint.
  "$TROND_BIN" apply --intent "$INTENT" --auto-approve --wait --output json
  log "apply done. Next: ./scripts/poc-shadow-fork.sh observe"
}

cmd_observe() {
  ensure_trond
  log "polling JSON-RPC for block height every 5s (Ctrl-C to stop)"
  local prev=0 count=0 iter=0
  for _ in $(seq 1 60); do
    iter=$((iter + 1))
    # NOTE: the shadow-fork chain produces blocks every 3s with a
    # single witness — we should see height climb on each poll.
    local raw height
    raw=$(curl -s http://localhost:8545 \
      -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
      2>/dev/null || echo "")
    height=$(echo "$raw" | sed -n 's/.*"result":"0x\([^"]*\)".*/\1/p')
    if [[ -z "$height" ]]; then
      # After ~1 minute of silence, dump the raw RPC reply once so
      # the operator sees actual errors (RPC disabled, missing
      # method, container still booting) instead of just "waiting…".
      if [[ "$iter" -eq 12 ]]; then
        printf '[poc-shadow-fork] node still not responding with a block height after 60s.\n'
        printf '[poc-shadow-fork] raw curl reply: %s\n' "${raw:-<empty>}"
        printf '[poc-shadow-fork] check container readiness: trond logs %s\n' "$NODE_NAME"
      else
        printf '[poc-shadow-fork] node not ready yet (waiting…)\n'
      fi
      sleep 5
      continue
    fi
    local dec=$((16#$height))
    if [[ "$dec" -gt "$prev" ]]; then
      count=$((count + 1))
      log "block height $dec ($(( dec - prev )) new since last poll)"
      prev=$dec
    fi
    if [[ "$count" -ge 5 ]]; then
      log "PASS — observed $count height increments, shadow-fork chain is producing blocks"
      return 0
    fi
    sleep 5
  done
  err "FAIL — chain did not advance after 5 minutes (check 'trond logs $NODE_NAME')"
}

cmd_teardown() {
  ensure_trond
  log "removing shadow-fork node (data dir preserved at $DATA_DIR for re-apply)"
  "$TROND_BIN" remove "$NODE_NAME" --confirm "$NODE_NAME" || true
  log "teardown done. Re-apply with: ./scripts/poc-shadow-fork.sh apply"
}

cmd_all() {
  cmd_setup
  cmd_mutate
  cmd_apply
  cmd_observe
}

main() {
  local action="${1:-}"
  case "$action" in
    setup)    cmd_setup ;;
    mutate)   cmd_mutate ;;
    apply)    cmd_apply ;;
    observe)  cmd_observe ;;
    teardown) cmd_teardown ;;
    all)      cmd_all ;;
    "")       err "usage: $0 {setup|mutate|apply|observe|teardown|all}" ;;
    *)        err "unknown action $action (want setup|mutate|apply|observe|teardown|all)" ;;
  esac
}

main "$@"

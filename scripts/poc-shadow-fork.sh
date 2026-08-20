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
# Chain + witness count are configurable:
#   SHADOW_FORK_NETWORK=mainnet        fork mainnet instead of Nile
#   SHADOW_FORK_WITNESS_COUNT=27       run a full slate
#
# Defaults stay nile / 1 witness, which is the cheap PoC.
#
# Caveats:
#   - One witness = no finality. The chain produces blocks every 3s
#     but the SR confirmation count stays at 1, well below the 2/3 a
#     block needs before java-tron calls it irreversible. Enough to
#     prove the mutation worked; not enough for anything that reads
#     solidified state, or for reproducing a solidification stall.
#     Raise SHADOW_FORK_WITNESS_COUNT for that.
#   - A mainnet lite snapshot is ~90 GB, against ~10-20 GB for Nile.
#     Each extra witness costs another full copy of it, because
#     java-tron opens its LevelDB exclusively and the nodes cannot
#     share a directory.
#   - The witness keypair comes from `trond shadow-fork keygen`, or
#     from the operator via the SHADOW_FORK_WITNESS_KEY and
#     SHADOW_FORK_WITNESS_ADDRESS env vars.
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
# Which chain the snapshot comes from. The intent's `network` has to
# match it: java-tron checks the genesis block against the network's
# HOCON at boot, and a mismatch crash-loops with "Genesis block modify".
NETWORK="${SHADOW_FORK_NETWORK:-nile}"
# How many witnesses the forked chain runs. One produces blocks but
# never solidifies them — the confirmation count sits at 1, far below
# the 2/3 a block needs before java-tron calls it irreversible. Raise
# this when the thing under test reads solidified state.
WITNESS_COUNT="${SHADOW_FORK_WITNESS_COUNT:-1}"
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
  # address. `trond shadow-fork keygen` does both, so this needs nothing
  # beyond the binary the script already requires. A caller who has their
  # own key can supply it through the env vars instead.
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
  log "generating fresh witness keypair via trond shadow-fork keygen"
  # trond writes the file itself, 0600 from the first byte, so the key is
  # never on disk world-readable — not even for the length of the write.
  # .gitignore keeps the path out of commits; the mode is what protects
  # against shared filesystems and an accidental tar/zip.
  "$TROND_BIN" shadow-fork keygen --count "$WITNESS_COUNT" --out "$KEY_STASH" >/dev/null
  log "stashed at $KEY_STASH (mode 0600)"
}

# Look up the Nth witness address / key from the stash. keygen writes
# the first pair unsuffixed and numbers the rest from 2, so a
# single-witness stash from before --count existed still resolves.
witness_addr() { local n=$1; if [[ $n -eq 1 ]]; then printf '%s' "$SHADOW_FORK_WITNESS_ADDRESS"; else local v="SHADOW_FORK_WITNESS_ADDRESS_$n"; printf '%s' "${!v}"; fi; }
witness_key()  { local n=$1; if [[ $n -eq 1 ]]; then printf '%s' "$SHADOW_FORK_WITNESS_KEY";     else local v="SHADOW_FORK_WITNESS_KEY_$n";     printf '%s' "${!v}"; fi; }

# fork.conf's witnesses/accounts are arrays whose length follows
# WITNESS_COUNT, so they are generated rather than sed-substituted into
# a fixed template. Everything else in the template is still static.
write_fork_conf() {
  local now_ms=$1 next_ms=$2 i addr
  {
    printf '# Generated by scripts/poc-shadow-fork.sh — do not edit by hand.\n'
    printf '# %s witness(es); regenerate with SHADOW_FORK_WITNESS_COUNT=<n>.\n\n' "$WITNESS_COUNT"

    printf 'witnesses = [\n'
    for ((i = 1; i <= WITNESS_COUNT; i++)); do
      addr=$(witness_addr "$i")
      [[ -n "$addr" ]] || err "witness $i has no address in $KEY_STASH — delete it and re-run setup"
      printf '  {\n    address = "%s"\n    url     = "http://shadow-fork-%d.local"\n    voteCount = %d\n  }%s\n' \
        "$addr" "$i" $((100000000 - i)) "$([[ $i -lt $WITNESS_COUNT ]] && printf ,)"
    done
    printf ']\n\n'

    # Each witness signs blocks, so each needs a funded account.
    printf 'accounts = [\n'
    for ((i = 1; i <= WITNESS_COUNT; i++)); do
      addr=$(witness_addr "$i")
      printf '  {\n    address = "%s"\n    accountName = "shadow-fork-witness-%d"\n    balance = 100000000000\n  }%s\n' \
        "$addr" "$i" "$([[ $i -lt $WITNESS_COUNT ]] && printf ,)"
    done
    printf ']\n\n'

    # Move the head to now so the node does not replay hours of slot
    # catch-up before producing.
    printf 'latestBlockHeaderTimestamp = %s\n' "$now_ms"
    printf 'maintenanceTimeInterval    = 21600000\n'
    printf 'nextMaintenanceTime        = %s\n' "$next_ms"
  } > "$FORK_CONF"
}

# The intent carries one node per witness. Ports step by one per node
# so the host bind does not collide; 8091/8092 are skipped because
# java-tron uses them in-container for solidity/PBFT and trond rejects
# an intent that maps onto them.
write_intent() {
  local i http grpc jsonrpc p2p metrics
  {
    printf '# Generated by scripts/poc-shadow-fork.sh — do not edit by hand.\n'
    printf '#\n'
    printf '# network MUST match the snapshot: java-tron validates the genesis\n'
    printf '# block against the network HOCON at boot and a mismatch crash-loops\n'
    printf '# with "Genesis block modify". Isolation from real peers comes from\n'
    printf '# the empty seed list and the bumped p2p version below, not from the\n'
    printf '# network name.\n'
    printf 'name: %s\n\n' "$NODE_NAME"
    printf 'target:\n  type: local\n  runtime: docker\n\n'
    printf 'network: %s\n\n' "$NETWORK"
    printf 'nodes:\n'
    for ((i = 1; i <= WITNESS_COUNT; i++)); do
      http=$((8090 + (i - 1) * 10))
      grpc=$((50051 + i - 1))
      jsonrpc=$((8545 + i - 1))
      p2p=$((18888 + i - 1))
      metrics=$((9527 + i - 1))
      printf '  - type: witness\n'
      printf '    witness_key:\n      private_key_env: %s\n' "$(witness_env_name "$i")"
      printf '    features:\n      jsonrpc: true\n      metrics: true\n'
      printf '    ports:\n      http: %d\n      grpc: %d\n      jsonrpc: %d\n      p2p: %d\n      metrics: %d\n' \
        "$http" "$grpc" "$jsonrpc" "$p2p" "$metrics"
      printf '    storage:\n      data: %s\n      logs: %s\n' \
        "$(node_data_dir "$i")" "$DATA_DIR/logs-$i"
      printf '    network_overrides:\n      need_sync_check: false\n'
      printf '    config_overrides:\n      "seed.node.ip.list": []\n      "node.p2p.version": 99999\n'
    done
  } > "$INTENT"
}

# The env var name each node reads its signing key from.
witness_env_name() { local n=$1; if [[ $n -eq 1 ]]; then printf 'SHADOW_FORK_WITNESS_KEY'; else printf 'SHADOW_FORK_WITNESS_KEY_%d' "$n"; fi; }

# Every node needs its own copy of the mutated database — they cannot
# share one directory. Node 1 uses the snapshot in place; the rest are
# copies made at apply time.
node_data_dir() { local n=$1; if [[ $n -eq 1 ]]; then printf '%s/output-directory' "$DATA_DIR"; else printf '%s/output-directory-%d' "$DATA_DIR" "$n"; fi; }

expand_templates() {
  # shellcheck source=/dev/null
  source "$KEY_STASH"
  local now_ms next_ms
  now_ms=$(($(date +%s) * 1000))
  next_ms=$((now_ms + 60000)) # 1 minute in the future

  write_fork_conf "$now_ms" "$next_ms"
  log "wrote $FORK_CONF ($WITNESS_COUNT witness(es), latestBlockHeaderTimestamp=$now_ms)"

  write_intent
  log "wrote $INTENT (network=$NETWORK, $WITNESS_COUNT node(s))"
}

cmd_setup() {
  ensure_trond
  mkdir -p "$DATA_DIR"

  if [[ ! -d "$DATA_DIR/output-directory/database" ]]; then
    log "downloading Nile snapshot to $DATA_DIR (this takes ~30 min)"
    "$TROND_BIN" snapshot download --network "$NETWORK" --type lite --to "$DATA_DIR"
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

  # Every witness runs its own java-tron, and java-tron holds the
  # LevelDB files exclusively — the nodes cannot share one directory.
  # Copy the mutated state once per extra witness. This happens after
  # the mutation so each copy already carries the new witness slate.
  local i dest
  for ((i = 2; i <= WITNESS_COUNT; i++)); do
    dest=$(node_data_dir "$i")
    if [[ -d "$dest" ]]; then
      log "node $i data dir already present: $dest (delete to refresh)"
      continue
    fi
    log "copying mutated state for witness $i → $dest"
    cp -R "$DATA_DIR/output-directory" "$dest"
  done

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

  # --auto-approve: setup regenerates latestBlockHeaderTimestamp on
  # each invocation, so the intent hash changes; without it a second
  # `apply` fails with HUMAN_REQUIRED (exit 10).
  if [[ "$WITNESS_COUNT" -gt 1 ]]; then
    # More than one node means the peers have to be wired to each
    # other, which is what `network create` does; `apply` deploys a
    # single node and would leave them unable to find one another.
    log "launching $WITNESS_COUNT-witness shadow-fork network from $INTENT"
    "$TROND_BIN" network create --intent "$INTENT" --output json
  else
    # --wait blocks until the container reports healthy so observe
    # doesn't poll an unborn JSON-RPC endpoint.
    log "launching shadow-fork node from $INTENT"
    "$TROND_BIN" apply --intent "$INTENT" --auto-approve --wait --output json
  fi
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

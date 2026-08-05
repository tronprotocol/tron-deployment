# AGENTS.md — for AI agents that call `trond`

This file is **for agents that USE trond as a tool** (Claude, ChatGPT,
Cursor, Cline, autonomous workflows, CI bots, etc.). Agents editing
this repository should also start here, then read the package-level
doc comments under `internal/` for architecture detail.

The contract here is meant to be self-contained: an agent that reads
this file once should be able to deploy, diagnose, and recover TRON
nodes without trial-and-error grepping through `--help` output.

---

## When to use trond

Use trond when the task involves any of:

- Deploying a TRON fullnode / witness / solidity / lite node (mainnet,
  Nile testnet, or private network), local Docker or remote SSH.
- Inspecting node state: sync progress, peer count, block height,
  endpoints, logs.
- Diagnosing a node that's misbehaving (the user says "my node isn't
  syncing" / "TPS is bad" / "it crashed").
- Bringing up a multi-node private network for testing.
- Pulling an official chain-database snapshot (mainnet sync from genesis
  takes days; snapshot drops it to hours).
- Verifying release artifacts (cosign signature, sha256 checksum).

Do **not** use trond for:

- Smart contract deployment, transaction signing, wallet operations —
  that's other tooling (`tronbox`, `tron-cli`, etc.).
- Editing java-tron source code or building it from scratch — trond
  consumes the official `tronprotocol/java-tron` Docker image / jar.
- Writing the intent file from a totally blank prompt — first ask what
  network / node type / target the user wants. trond requires a
  declarative `intent.yaml`; guessing is more harmful than asking.

---

## Universal calling convention

Always pass `-o json` (or `--output json`) and parse the structured
response. Text mode is for humans; agents should never grep stdout.

Always set `TROND_STATE_DIR` for concurrent / multi-agent scenarios so
sessions don't collide on `~/.trond/state.json`:

```bash
export TROND_STATE_DIR=/tmp/trond-$AGENT_SESSION_ID
trond <command> -o json
```

Set `auto_ports: true` under `target:` in intent files when the host
might already be running TRON nodes — trond allocates free OS ports
and persists them so the agent doesn't have to negotiate ports.

For long-running agent sessions or distributed orchestration, set
`TROND_OTLP_ENDPOINT` to ship spans (one per `trond <cmd>` invocation)
to your OpenTelemetry collector:

```bash
export TROND_OTLP_ENDPOINT=https://otel.example.com:4318
# Optional: TROND_OTLP_HEADERS="api-key=...,tenant=..."
# Optional: TROND_OTLP_INSECURE=1   (allow plaintext HTTP)
# Optional: TROND_OTLP_TIMEOUT=10s  (shutdown flush budget)
```

Unset (default) → no-op tracer, zero overhead, no network IO.

---

## Exit codes — what each means and how to react

| Code | Meaning | Recommended retry strategy |
|---|---|---|
| 0 | success | continue to next step |
| 1 | general error (`WAIT_TIMEOUT`, `EXEC_ERROR`, `NODE_NOT_FOUND`, etc.) | inspect `error_code` in the JSON, follow `suggestions[]`; if transient (network), retry once with exponential backoff |
| 2 | validation error in intent.yaml | DO NOT retry — fix the intent file, then re-run |
| 3 | target unreachable (SSH/Docker connection) | one retry after 5s; if still failing, surface to user with the host/connection detail |
| 4 | preflight failure (missing dependency on target) | DO NOT retry — present `missing_components` array to user; offer `trond bootstrap` |
| 10 | `HUMAN_REQUIRED` — destructive op needs explicit confirmation | DO NOT silently retry with `--auto-approve` / `--confirm` unless the user has authorised that for this session |

Every error JSON has the same shape:

```json
{
  "error_code": "VALIDATION_ERROR",
  "exit_code": 2,
  "message": "intent.yaml line 12: target.type must be one of [local ssh]",
  "suggestions": [
    "Set target.type to 'local' for local Docker deploys",
    "Or 'ssh' for remote deploys; also set target.host and target.user"
  ]
}
```

Process `suggestions[]` in order. The first entry is the most
prescriptive and usually fixes the issue automatically; later entries
are alternative fixes for less-likely causes.

---

## Workflow 1 — Deploy a node from scratch

The canonical chain. Each step's output feeds the next; bail on any
non-zero exit unless explicitly told to continue.

```bash
# 1. Validate the intent shape — catches typos before any network IO.
trond config validate intent.yaml -o json
# Output: {"valid": true, "name": "my-fullnode", "network": "mainnet", "node_count": 1}
# Bail on exit 2.

# 2. Preflight the target — missing docker, low memory, port conflicts.
trond preflight --intent intent.yaml -o json
# Output: {"target": "local", "checks": [...], "overall": "pass" | "warning" | "fail"}
# Bail on exit 4. On "warning" surface details to user but proceed.

# 3. Plan — show what apply WOULD do without executing.
trond plan --intent intent.yaml -o json
# Output: {"name": "...", "current_state": "not_deployed" | "running" | ...,
#          "changes": [{"type":"create"|"update"|"delete", "field":"...", ...}],
#          "destructive": false, "estimated_downtime_seconds": 0}
# If changes is empty AND current_state is "running", you can skip apply.
# Add --diff to also surface the line-by-line HOCON config delta:
#   trond plan --intent intent.yaml --diff -o json
# Output: same shape plus "config_diff": ["+ key = newValue", "- key = oldValue", ...]

# 4. Apply — idempotent. Re-running the same intent is a no-op.
trond apply --intent intent.yaml --auto-approve --wait -o json
# Output: {"name":"...", "result":"created"|"updated"|"no_change",
#          "endpoints":{"http":"http://127.0.0.1:8090","grpc":"127.0.0.1:50051"},
#          "duration_ms": 3500, "ready": true}
# --wait blocks until the HTTP API responds. Without --wait, the
# container is started but may not yet be syncing.
# On exit 10 (HUMAN_REQUIRED): surface the diff to the user, ask for
# approval, then retry with --auto-approve. NEVER silently re-run.

# 5. Verify — post-deploy health gate. Polls until block_height > 0.
trond verify --intent intent.yaml --timeout 5m -o json
# Output: {"name":"...", "verified": true, "block_height": 12345678,
#          "attempts": 3}

# 6. Status — readable summary the agent shows the user.
trond status my-fullnode -o json
# Output: {"name":"...", "status":"running", "block_height": ...,
#          "peer_count": 12, "is_synced": true, "healthy": true,
#          "container_id": "<64-hex>", "api_endpoints":{...},
#          "logs": {"runtime":"docker","container":"...","path":"/java-tron/logs/tron.log"}}
```

**Machine-observable rig state (status / inspect `-o json`):**
- `healthy` (bool) — liveness gate: "is the RPC serving blocks?". Always
  present; `true` only when `status=running` AND the live `getnowblock`
  probe returned a REAL block (non-zero block timestamp — an empty/error
  200 does not count). It does NOT mean fully synced: `healthy` can be
  `true` while `is_synced` is `false` on a node whose tip is stale, so gate
  liveness on `healthy` and sync on `is_synced`/`block_height`. Fail-safe
  `false` when stopped, unreachable, or the probe was skipped.
- `container_id` (string) — full 64-hex docker container ID, so an agent
  can `docker exec`/attach the exact container. Best-effort; present for
  running/stopped docker nodes, absent for jar nodes or when the daemon
  is unreachable.
- `logs` (object) — runtime-discriminated locator for reading node logs
  without screen-scraping (point `mcp-logs` here). `runtime=docker`:
  `docker exec <container> cat <path>` (java-tron logs to a file *inside*
  the container, not stdout). `runtime=jar`: `journalctl -u <unit>`.
- `api_endpoints.http` is the private-net RPC — point read-only on-chain
  readers (e.g. `tron-toolkit`) at it for the verify half of a loop.
- `monitoring` (object, when deployed with `--monitor`) — the node's
  Prometheus + Grafana stack ports (`enabled`, `prometheus_port`,
  `grafana_port`). Present in `status` / `inspect -o json`; host is implicit
  (127.0.0.1 local, `target.host` for ssh). Lets an agent point a metrics
  reader at the stack without parsing the apply result.
- `build_revision` (+ `build_cache_key`) — for a node built from source
  (intent `build:` block), the resolved java-tron git revision (short
  12-hex) it's running, so an agent never has to assume which commit is
  deployed. Present in `status` / `inspect -o json`; `list` carries the
  raw `build_cache_key`. Absent for pre-built image/jar nodes.
- `genesis_block_id` (status, live-only) — the chain's identity fingerprint:
  the block-0 TRON block id (64-hex) from a live probe. Constant for the
  chain's lifetime; use it to tell private rigs apart and correlate
  on-chain. Absent when the node is stopped/unreachable (like block_height).
  This is NOT `chain_id`: TRON's TVM `CHAINID` is the full block id unless
  certain VM flags are on (then the last 4 bytes), so a flag-aware `chain_id`
  is still deferred — tracked in TODOS.md.

**Idempotency**: `apply` is hash-gated. Same intent → no-op. Changed
intent without `--auto-approve` → exit 10. The agent should always
diff first via `plan` and only pass `--auto-approve` when the user
approved the changes shown by `plan`.

---

## Workflow 2 — Diagnose a misbehaving node

When the user complains "my node is broken / not syncing / slow",
collect structured facts before suggesting fixes.

```bash
# 1. Check if the process is even alive at the runtime layer.
trond status <node-name> -o json
# Look at: status (running/stopped/error), is_synced, lag (block delta),
# peer_count. If status != "running", go to logs+health straight away.

# 2. Run the structured health/diagnose suite.
trond diagnose <node-name> -o json
# Output: {"checks": [
#   {"name":"sync_progress", "status":"pass"|"warning"|"fail", "message":"...", "suggestions":[...]},
#   {"name":"peer_count", ...},
#   {"name":"disk_space", ...},
#   {"name":"port_listening", ...},
#   ...
# ], "overall":"..."}
# Each check carries its own suggestions[]. Process failed checks first.

# 3. If diagnose isn't conclusive, look at logs.
trond logs <node-name> --tail 200 -o json
# Output: {"lines":[{"ts":"...","level":"WARN","message":"..."}]}
# Pattern-match common signatures: "Multiple garbage collectors selected",
# "private key must be 64 hex string", "Connection refused", "out of memory".

# 4. If a remediation exists, propose it. Don't silently apply changes.
#    Common remediations:
#    - peer_count=0 → check seed nodes / network_overrides.seeds
#    - is_synced=false but lag is shrinking → wait, not broken
#    - is_synced=false and lag growing → restart, then diagnose again
#    - disk_space.fail → user needs to free space; trond can't fix this
```

The `events` audit log is also worth pulling for context:

```bash
trond events --since 1h -o json
# JSONL stream of past commands: who ran what, when, what changed.
```

---

## Workflow 3 — Skip genesis sync via snapshot

A fresh mainnet fullnode otherwise spends days catching up. Snapshots
drop it to hours.

```bash
# 1. Show available mirrors so the user (or you) can pick.
trond snapshot sources -o json
# Output: {"sources":[{"network":"mainnet","kind":"lite","region":"singapore",
#                       "domain":"34.143.247.77","approx_size_gb":46, ...}, ...]}

# 2. Dry-run to confirm disk space + URL + would-overwrite check.
trond snapshot download --network mainnet --to /srv/tron/<node> --dry-run -o json
# Output: {"preflight":{"expected_size_bytes":...,"free_bytes":...,
#          "needed_bytes":...,"would_overwrite":false,"has_md5_sidecar":true,
#          "plaintext_transport":true}, ...}
# If needed_bytes > free_bytes → tell user, abort.
# If would_overwrite → ask user before passing --force.
# If plaintext_transport is true → say so before proceeding (see below).

# 3. Start the actual download in background.
trond snapshot download --network mainnet --to /srv/tron/<node> --detach -o json
# Output: {"job_id":"20260501-103300-abc1","pid":12345,"log_path":"...",
#          "dest":"/srv/tron/<node>","backup":"backup20260501","kind":"lite"}

# 4. Poll until done. The download can take hours for mainnet full.
while true; do
  STATUS=$(trond snapshot jobs -o json | jq -r ".jobs[] | select(.id==\"$JOB_ID\")")
  RUNNING=$(echo "$STATUS" | jq -r .running)
  if [ "$RUNNING" = "false" ]; then break; fi
  sleep 60
done
# Or stream progress:  trond snapshot logs <job_id> -f
# Cancel a running detached job:  trond snapshot stop <job_id> -o json

# 5. Once done, the chain DB sits at <dest>/output-directory/database.
#    Now apply with an intent whose storage.data points there.
trond apply --intent intent-with-snapshot.yaml --auto-approve --wait -o json
```

### `plaintext_transport` — do not skip this

The six mainnet mirrors are bare IPs that publish **no HTTPS endpoint**
(probed 2026-07-31: `:443` closed, `:80` answers). Only the nile mirror
serves TLS. Both the `--dry-run` `preflight` object and the completion
payload carry `"plaintext_transport": true` for those transfers, and trond
prints a warning to stderr (which, under `--detach`, lands in the job log).

What that means when you report to a user: **`"md5_verified": true` is not
a security statement on those mirrors.** The `.md5sum` sidecar arrives over
the same unauthenticated connection as the tarball, so anyone able to
substitute the archive can substitute the sidecar too. Do not tell a user
the snapshot is "verified" on the strength of that field alone.

The completion payload also carries `"sha256"` (always computed) and
`"sha256_verified"` (true only when the operator supplied a pin **and** it
matched):

```bash
# Record the digest on a fetch you have reason to trust...
trond snapshot download --network mainnet --to /srv/a -o json | jq -r .sha256

# ...then pin it on later fetches of that same backup.
trond snapshot download --network mainnet --backup backup20250115 \
  --to /srv/b --sha256 <hex> -o json
# A mismatch fails the download with "sha256 mismatch: ...".
```

A pin is only worth the channel you got it over — a digest read off the
same cleartext mirror buys nothing. Surface `plaintext_transport` to the
user rather than deciding for them; if they have an out-of-band digest,
pass it through `--sha256` (or the MCP `snapshot_download` `sha256` arg).
If a download exits non-zero with `VERIFICATION_UNAVAILABLE`, the mirror's
`.md5sum` sidecar could not be fetched, so trond refused to extract a chain
database it cannot check — nothing was written to the destination. Retry,
or pick another backup/mirror. Only pass `--no-verify` (MCP:
`no_verify: true`) when the user has explicitly accepted an unverified
chain database; a completed download reports
`"verification_skipped": true` in that case.

When the download finishes, its manifest + log stay under
`~/.trond/snapshots/<id>.{json,log}` so you can audit later. Stale
ones (default: stopped + older than 7 days) can be reclaimed with:

```bash
trond snapshot prune --dry-run            # preview
trond snapshot prune                      # default policy
trond snapshot prune --all -o json        # ignore age, return JSON
```

### Warm pool — `trond snapshot clone`

Once you have a chain DB on disk (a downloaded snapshot, or a STOPPED
node's data dir), fork it into isolated fixtures in seconds instead of
re-downloading 30-90GB:

```bash
trond snapshot clone ./output-directory ./rig-a/output-directory -o json
# → {"source":"...","dest":"...","method":"clonefile","duration_ms":3}
```

`method` is `clonefile` (macOS APFS) or `ficlone` (Linux btrfs/xfs) for a
copy-on-write clone — instant, sharing extents, near-zero extra disk. It is
`copy` when source and dest are on different filesystems (or a non-CoW FS):
a full byte copy is made (slow, full disk) and a warning prints to stderr,
so keep the clone target on the SAME filesystem as the source for the fast
path. `<dst>` must not already exist (clone refuses rather than overwrite),
and the source must be **quiescent** — stop a node before cloning its live
DB, or the point-in-time view is undefined. Cross-FS clones get a
free-space preflight (refused with `DISK_SPACE_ERROR` if the copy won't
fit). CLI-only (not an MCP tool): it mutates the filesystem, so it stays
out of the read-only agent fleet.

Or fork a managed node's DB by name with `--from-node` (resolves the path
from state, so you don't hand-type it):

```bash
trond snapshot clone --from-node fn0 ./rig-a/output-directory -o json
```

`--from-node` requires the node be **stopped** and on a **local** target,
and clones the node's chain-DB dir verbatim into `<dst>`. It works for jar
nodes (`<install_path>/output-directory`) and docker nodes that use
**bind-mount** storage (`storage.path` / `storage.data`). A docker node on
the **default named volume** is refused — a named volume isn't a host path
and can't be copy-on-write cloned; redeploy with `storage.path`, or pass an
explicit `<src>`. ssh-target nodes are refused (the clone runs locally;
remote-side clone is a tracked TODO).

The intent needs `storage.data: /srv/tron/<node>/output-directory` so
the bind mount lines up with where the tarball extracts. See
`examples/mainnet-fullnode-snapshot.yaml`.

**Why the two-stage flow**: `apply` is supposed to be seconds-fast and
idempotent. A multi-hour download inside apply would break the contract
for every CI / orchestration caller. Keep them separate.

---

## Workflow 4 — Multi-node private network

For testing, demos, or development against a private chain.

```bash
# 1. Use the example as a starting point.
cp examples/private-network.yaml my-net.yaml
# Edit witness count / fullnode count / per-node memory.

# 2. Validate.
trond config validate my-net.yaml -o json

# 3. Preflight (each node target gets checked).
trond preflight --intent my-net.yaml -o json

# 4. Create the whole network in one shot. trond auto-wires
#    node.active between siblings so peering works under auto_ports.
SR_KEY=da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0 \
  trond network create --intent my-net.yaml --wait -o json
# Output: {"network":"pn", "nodes":[{"name":"pn-node0", "endpoints":{...}}, ...]}

# 5. Inspect / probe / chaos-test. `network status` lists every managed
#    node whose name matches the *-node* pattern (a flat array — no
#    network-name filter today; use `trond list` for full state).
trond network status -o json
trond inspect --network pn -o json
trond exec pn-witness -- /java-tron/bin/FullNode --help

# 6. Rolling upgrade — fullnodes first then witnesses, gated by verify.
#    --auto-rollback reverts every already-upgraded node if any fails.
trond network upgrade pn \
  --intent my-net.yaml \
  --version 4.8.1 \
  --auto-rollback -o json
# Output: {"network":"pn","version":"4.8.1","upgraded_count":N,
#          "upgraded_nodes":[...],"steps":[{node,phase,status,...}],
#          "status":"success"|"failed","failed_at":"..."?}

# 7. Cleanup. --confirm must match the network name (refuses typos).
trond network destroy pn --confirm pn -o json
```

---

## Workflow 5 — Shadow-fork testing on a real snapshot

Use when the user wants to test a hard-fork proposal, a witness-set
transition, or a contract-state migration against realistic
mainnet/Nile state — without affecting the real network. Takes a
chain DB snapshot, surgically replaces the witness set + funds test
accounts + tweaks DynamicProperties timestamps, then launches a
private network from the modified state.

The mutation engine is a Go port of java-tron's `DbFork` toolkit
(equivalence test runs in CI against a real Nile snapshot to prove
byte-equivalence). Both HOCON and YAML fork.conf formats accepted.

```bash
# 1. Pull a Nile snapshot. Mainnet works too; Nile is lighter and
#    faster for the demo path.
trond snapshot download --network nile --type lite \
    --to /srv/shadow-fork -o json

# 2. The node owning the data dir MUST be stopped before mutate
#    (LevelDB locks are exclusive). If you just downloaded a fresh
#    snapshot, no node is running — skip this. Otherwise:
trond stop <node> -o json
# Output: {"name":"<node>","previous_state":"running","new_state":"stopped",...}

# 3. Apply the fork.conf — replace witnesses, fund accounts, set
#    TRC10/TRC20 balances, adjust timestamps. The mutation is
#    in-place and idempotent (re-runs against the same conf
#    produce the same on-disk state).
trond shadow-fork mutate \
    -d /srv/shadow-fork/output-directory \
    -c fork.conf -o json
# Output: {"witnesses_written":1, "active_witnesses":1,
#          "accounts_modified":1, "trc20_slots_updated":0,
#          "properties_updated":3, "duration_ms":...}

# 4. Launch a private shadow-fork node off the mutated dir. The
#    intent MUST use `network: nile` (the snapshot's genesis hash
#    must match the base config) AND isolate via:
#    - network_overrides.need_sync_check: false
#    - config_overrides."seed.node.ip.list": []
#    - config_overrides."node.p2p.version": 99999  (foreign chain)
trond apply --intent shadow-fork-intent.yaml --auto-approve --wait -o json

# 5. Verify the shadow chain is producing blocks. Single-witness
#    setups produce a block every 3s (every slot is that witness's).
trond status <node> -o json
# Output: latest_block_number climbs each invocation.
```

See `knowledge/shadow-fork-poc.md` + `examples/shadow-fork/` for
the full walkthrough including the witness-keypair generation step
(tronpy) + the canonical fork.conf template. `scripts/poc-shadow-fork.sh`
orchestrates the whole flow.

### Key invariants

- **fork.conf is the contract**. Per-section semantics live in
  `internal/dbfork/{witnesses,accounts,trc20,properties}.go` and
  mirror java DbFork verbatim. Operators reading the conf in either
  HOCON or YAML get identical Config shapes.
- **Genesis hash must match**. The shadow-fork intent's `network:`
  determines the base config's genesis block. A Nile snapshot
  requires `network: nile`; a mainnet snapshot requires
  `network: mainnet`. Mismatch crash-loops the container with
  `Genesis block modify, please delete database directory`.
- **The node must be stopped** before mutate. `shadow_fork_mutate`
  fails loudly with `resource temporarily unavailable` if a
  process holds the LevelDB lock — but the failure is mid-mutation
  and may leave partial writes. Always `trond stop` first.
- **Single-witness setups produce blocks but lack finality**. The
  per-block SR confirmation count stays at 1 (well below the 19/27
  threshold). Good enough to verify mutations applied; not
  sufficient for testing finality-dependent contract logic. Real
  shadow-fork production parity uses 27 witnesses.

---

## Workflow 6 — Build java-tron from source

Use when the user wants a custom build: a fork, an unreleased patch,
a wired-in profiler. The build pipeline is content-addressed by
git revision + builder image digest + gradle task + args, so
repeated `apply` calls against an unchanged source are sub-second
cache hits, not full recompiles.

Trigger: the intent file carries a `build:` block under the node.
`trond apply` invokes the build automatically; you rarely need to
call `trond build` directly except for cache mgmt or pre-warming.

```bash
# 1. Validate the intent like any other.
trond config validate my-dev.yaml -o json

# 2. Preflight gains LOCAL-side `build-*` checks when build: is set:
#    build-git, build-source-<dir>, build-docker-local (if builder=docker),
#    build-host-jdk + build-host-gradlew-<dir> (if builder=host).
#    Target-side checks (jdk, disk, ports) still run too.
trond preflight --intent my-dev.yaml -o json

# 3. Apply — the build kicks off automatically before the deploy step.
#    Cold first build: 3-5 min for java-tron. Cached: ~50ms.
trond apply --intent my-dev.yaml -o json
# Output now carries a `build` field alongside the usual envelope:
# {"name":"...", "result":"created",
#  "build": {"cache_key":"abc12345-bd…+dirty-7f2a…",
#            "source_revision":"8f4e2a3c…",
#            "dirty": true,
#            "artifact_path":"/home/.../FullNode.jar",
#            "sha256":"160185…", "cache_hit": false,
#            "duration_ms": 215000},
#  ...}

# 4. Survey what's in the cache.
trond build list -o json
# Output: {"count": N, "entries": [{cache_key, artifact_kind, size_bytes,
#                                   orphaned, source_revision, ...}, ...]}

# 5. Inspect a specific entry — full key OR unambiguous prefix.
trond build inspect 8f4e2a3c -o json
# Returns the single entry (same shape as build_list entries).
# Errors: NOT_FOUND (no match), AMBIGUOUS_PREFIX (multi-match;
# message lists candidates).

# 6. Prune. Filters AND together; dry-run is the default — only
#    --confirm performs deletions.
trond build prune --older-than 168h -o json                  # dry-run
trond build prune --older-than 168h --keep-last 3 --confirm  # delete
trond build prune --orphan --confirm                         # GC
```

### Building with patches

For systematic patching (test unmerged PRs, replay-style instrumentation,
compliance variants), declare patches in the intent — trond opens a
fresh `git worktree`, applies them there, builds, removes the worktree.
Your primary source tree is never mutated.

```yaml
build:
  source: ../java-tron
  patches:
    - ../tron-deployment/tools/replay/patches/01-skip-tx-expiration.patch
    - ../tron-deployment/tools/replay/patches/02-skip-tapos-validation.patch
```

Paths are intent-relative. When `patches:` is non-empty:

- `patch_hash` is a deterministic fold of per-patch SHA256s in declared
  order — a pure function of patch CONTENTS (not paths). Same intent +
  same patches → same `cache_key` on every machine (no longer dependent
  on local working-tree state). This is the ONLY way to get cross-machine
  cache reuse for non-vanilla builds.
- The build manifest records `patches: [{name, sha256}, ...]` — basename
  + content sha256, one entry per applied patch. `trond build inspect <key>`
  surfaces these so an agent can verify a teammate's local patch files
  match the cached artifact's inputs without depending on absolute paths.
- Validation surfaces `INVALID_PATCH` (path missing / not a unified diff)
  or `PATCH_FAILED` (apply rejected) as structured errors with
  `exit_code: 2`. Agent should re-read the patch path or update
  `build.revision` if the patch is against a different commit.
- For iterative dev where you're editing source actively, leave
  `patches:` empty and use the working-tree dirty path.

### Key invariants for agents

- The build runs on the LOCAL machine even when `target.type: ssh` —
  trond `scp`s the JAR over with an SHA256-skip fast-path. No source
  bytes ever leave the host.
- `build.builder: host` works without docker but pins the cache key
  to `sha256(java -version)`, so a JDK upgrade orphans prior entries
  (orphans are surfaced in `list --include-orphans` and cleaned by
  `prune --orphan`).
- The CLI rejects `--keep-last N --confirm` without an explicit
  scoping filter (`--all`, `--orphan`, or `--older-than > 0s`).
  This is a footgun guard: `--keep-last 1 --confirm` would wipe
  every entry except the newest, which is rarely what the user
  meant.
- Build *execution* is intentionally NOT exposed via MCP — it's a
  long-running, stderr-streaming operation. The CLI's `-o json`
  stream is the right surface. The cache-mgmt tools (`build_list`,
  `build_inspect`, `build_prune`) ARE exposed via MCP — see below.

Full walkthrough including image artifacts, cross-arch builds, and
the `image_strategy: jar-wrap` variant for stock java-tron:
[`specs/002-trond-build-pipeline/quickstart.md`](specs/002-trond-build-pipeline/quickstart.md).

---

## Test-harness primitives (for agents driving CI / fuzz / chaos)

These exist for agents that drive trond programmatically rather than
managing a single user's nodes.

```bash
# Block until a probe succeeds (port listen, HTTP endpoint, exec output).
trond wait <node> --port 8090 --timeout 60s -o json
trond wait <node> --http "http://127.0.0.1:8090/wallet/getnowblock" \
  --json '.block_header.raw_data.number > 100' --timeout 5m

# Exec arbitrary commands inside a node (docker exec / SSH exec).
trond exec <node> -- ls /java-tron/output-directory

# Push/pull files. Use for keystores, custom configs, snapshots.
trond files put <node> ./local.conf /java-tron/conf/custom.conf
trond files get <node> /java-tron/logs/tron.log ./tron.log

# Chaos primitives — works at the docker network layer.
trond disconnect <a> <b>          # tear down peer link
trond connect    <a> <b>          # restore
trond partition  --groups 'a,b|c,d'  # split network into partitions
trond heal                        # restore everything

# Audit feed. Useful when an agent wants to see what it did earlier.
trond events --since 1h --follow -o json
```

---

## Concurrency and isolation

Each agent session should pin its own state-dir:

```bash
TROND_STATE_DIR=/tmp/trond-${SESSION_ID} trond <command>
# state.json, audit.log, deployments/, snapshots/ all rooted here
```

This is the ONLY supported way to run two agent sessions on the same
host without corrupting state. The default `~/.trond/` is single-user.

`auto_ports: true` in the intent's target block solves port collisions
when multiple sessions deploy on the same host.

---

## Knowledge / reference inside trond

trond ships embedded markdown topics — fetch by name when you need
deeper context than this file:

```bash
trond knowledge                       # list topics
trond knowledge <topic> -o json       # one topic
```

Topics include: `node-types`, `troubleshooting`, `best-practices`,
`config-reference`, `cloud-deployment`, `test-harness`, `snapshots`,
`release-signatures`. Read these when the user's question maps to a
topic; don't paraphrase from training data.

---

## Safety — proving a rig is private (for unattended agents)

An automated caller that should only ever touch a throwaway private chain
(never mainnet/nile) can both **prove** and **enforce** that:

- **Query the fact.** `status`, `apply`, `list`, and `inspect` (`-o json`)
  all return `network` (`mainnet | nile | private`) and `is_private`
  (bool). `is_private` is `true` only when `network == "private"`; it is
  `false` for an empty/unknown network — fail-safe, so "I'm not sure"
  reads as "not safe to mutate". Gate destructive actions on
  `is_private == true`. (It is a LABEL check: a `private` intent that
  re-points `seed.node.ip.list` at mainnet via `config_overrides` would
  still report `is_private:true`. Deeper config-sniffing is a tracked
  TODO; standard private rigs use the private template, which ships no
  real seeds.)

- **Enforce on every mutation.** `--require-private` is a **persistent
  flag** (works on any command) plus the **`TROND_REQUIRE_PRIVATE`** env
  var. When set, every mutating verb refuses a non-private node up front
  with `PRIVATE_NETWORK_REQUIRED` (exit 2): `apply`, `network create`, and
  the per-node mutators `start` / `stop` / `restart` / `remove` /
  `rollback` / `upgrade` / `auto-heal`, plus the three that run caller-supplied
  content against a node: `exec`, `wait --exec` (which is `sh -c <string>` on
  the node) and `files put` (which writes caller bytes to a caller path,
  on a jar node to the target host). Those three are gated regardless of
  what the command or file happens to contain — the capability is the
  thing being gated, not the payload. Their read counterparts stay
  allowed: `wait --port` / `wait --http` / `files get` change nothing and
  refusing them would only make the gate something people switch off.
  A test derives this list from the source
  (`cmd/require_private_coverage_test.go`), so a new command that resolves
  a node must either take the guard or record why it needs none. The MCP `apply` tool takes the same
  gate via a `require_private` argument, and the MCP `auto_heal` tool honours
  the flag/env floor. Proposal-only calls stay allowed: `auto-heal --dry-run`
  / `auto_heal dry_run=true` never starts a node or writes state, so the
  preview still returns under the gate. Export `TROND_REQUIRE_PRIVATE=1` once at
  session start and an unattended agent is *mechanically* incapable of
  mutating a mainnet/nile rig — no per-call discipline, instead of trusting
  a standing rule.
- **It's a one-way floor, not a fallback.** The flag OR a truthy env turns
  the gate on; once on it cannot be turned off for the invocation
  (`--require-private=false` cannot override a truthy env). Pure opt-in —
  default behaviour and mainnet deploys are unchanged when neither is set.
- The guard checks the node's RECORDED network from state, using state
  ONLY (before any SSH/target work), so an unreachable mainnet node still
  refuses with `PRIVATE_NETWORK_REQUIRED` rather than `TARGET_UNREACHABLE`.
  A node with no recorded network (deployed before network tracking) is
  fail-safe-refused with a re-apply hint.
- `apply` is gated on BOTH networks: the intent's `network:` label AND the
  recorded network of the node already deployed under that `name:`. An
  intent cannot re-label a mainnet/nile node as `private` to get at it —
  that refuses with `PRIVATE_NETWORK_REQUIRED` under the gate, and with
  `NETWORK_MISMATCH` (exit 2) even when the gate is off, so a node's
  recorded network is never silently downgraded to `private`. Deploying a
  brand-new node is unaffected, and re-applying an intent whose network
  matches the deployed one behaves exactly as before. To genuinely convert
  a node between networks, `trond remove` it first, then apply.
- `network add` is gated on BOTH networks: the intent's own `network:`
  label AND the recorded networks of the nodes already in the enclave
  named by `--network`. A `network: private` intent therefore cannot add
  a node into a mainnet/nile enclave (where it would be peered with the
  production nodes and scraped by their Prometheus) — that refuses with
  `PRIVATE_NETWORK_REQUIRED`, naming the offending member. Under the gate
  an enclave with no nodes recorded in state cannot be proven private, so
  adding to it is refused too; with the gate off, `add` behaves exactly as
  before.

The read-only verbs (`status`, `wait`, `inspect`, `list`, `diagnose`)
never mutate and are always safe. The multi-node mutators are now gated
too: the chaos primitives (`disconnect`/`connect`/`partition`/`heal`) and
`network add`/`destroy`/`upgrade` refuse unless EVERY node they touch is
private, naming the first offender. So with `TROND_REQUIRE_PRIVATE=1` set,
no trond verb — single-node or multi-node — will mutate a non-private rig.

> ⚠️ **`network` means two things across commands.** In `apply` / `status`
> / `list` / `inspect` it is the chain kind (`mainnet|nile|private`,
> matching the intent's `network:` field). In **`network create`** output
> it is the network's intent NAME. Don't carry the value from one across
> to the other. (A rename of `network-create`'s field is a tracked TODO —
> it's a breaking output-contract change, deferred to a deliberate bump.)

---

## Anti-patterns — things to NEVER do

1. **Don't silently retry HUMAN_REQUIRED with `--auto-approve`**. Exit
   10 means a destructive change requires user consent. Show the diff,
   ask, only then re-run. Hard rule, no exceptions.

2. **Don't skip preflight on remote SSH targets**. Missing Docker / JDK
   / disk space on the remote will turn a 3-second `apply` into a
   confusing failure mid-deploy. Run preflight first, surface failures.

3. **Don't grep `trond --help` to discover commands**. Use
   `trond knowledge` for tutorials, `trond config docs <key>` for HOCON
   keys, and `trond schema` for the full machine contract.

4. **Don't write witness keys into `extra_env`**. Use the
   `witness_key.private_key_env` field; trond inlines the value into
   the rendered HOCON at apply time. The container env path silently
   fails because typesafe-config doesn't substitute `${VAR}`.

5. **Don't bypass `auto_ports` and try to negotiate ports yourself**.
   trond does TCP+UDP free-port detection that handles the P2P
   discovery UDP requirement; rolling your own usually breaks under
   docker compose's port binding semantics.

6. **Don't use `--detach` for short downloads**. The job machinery has
   overhead. Foreground is fine for nile lite (~30 GB on a fast
   connection).

7. **Don't trust unsigned releases**. The release pipeline produces
   `checksums.txt` + `.sig` + `.pem` for every tag. Validation recipe
   is in `trond knowledge release-signatures` or README.

8. **Don't blow away `~/.trond/snapshots/<id>.json`** to clean up jobs.
   Use the upcoming `snapshot prune` (or just `rm` the JSON+log pair
   when the job is `state=stopped`).

9. **Don't `rm -rf` the build cache directory** (`$TROND_STATE_DIR/builds`,
   defaulting to `~/.trond/builds`) to clean it. Use `trond build prune`
   so the manifests, image side-files, and docker-stored images all get
   reclaimed consistently. Manual `rm` leaves dangling docker layers
   and orphaned manifests that confuse the next `trond build list`.

10. **Don't pass `--keep-last N --confirm` alone to prune**. The CLI
    rejects it for safety: that combo deletes everything except the N
    newest entries, which is rarely the intent. Combine with `--all`
    (to acknowledge near-wipe) or scope with `--orphan` / `--older-than`.
    Same guard applies to the MCP `build_prune` tool.

---

## Schema discovery

`trond schema [-o json]` dumps the full command tree + flag types +
output JSON Schemas as one machine-readable manifest. Pin against the
top-level `schema_version` field for stability.

```bash
# Full manifest (every command, every flag, every documented schema)
trond schema -o json | jq '.schema_version, (.commands | length)'

# One command's spec
trond schema apply -o json
trond schema "snapshot download" -o json

# Just the output JSON Schema for one command (useful for agent input
# validation when piping trond's output through `ajv` or similar)
trond schema apply --output-only -o json
```

## MCP server

For chat-based / IDE-embedded agents that can't shell out (Claude
Desktop, Cursor, Cline, Continue.dev, Zed AI, ChatGPT Apps), trond
runs as a Model Context Protocol server:

```bash
trond mcp        # speaks JSON-RPC over stdio
```

Configure once in your client. Example for Claude Desktop
(`~/.config/claude-desktop/config.json` or
`%APPDATA%\Claude\config.json`):

```json
{
  "mcpServers": {
    "trond": { "command": "/usr/local/bin/trond", "args": ["mcp"] }
  }
}
```

The server registers 23 tools (read-only unless marked):

- **inspection** (3): `list`, `status`, `inspect`
- **diagnostic** (4): `doctor`, `version`, `health`, `diagnose`
- **config** (3): `config_validate`, `config_render`, `verify_config`
  (read-only — pulls the live node's `.conf`, renders fresh HOCON from
  `--intent`, returns `diffs[]`; a cheap reconcile signal before
  deciding whether `apply` is warranted)
- **remediation** (1): `auto_heal` (destructive — runs the diagnostic
  suite then applies only safe, idempotent fixes to fail checks, e.g.
  start a stopped node; everything else surfaces in `skipped[]` with a
  human suggestion. Pass `dry_run=true` to propose without acting)
- **lifecycle** (2): `plan`, `apply` (destructive)
- **snapshot** (4): `snapshot_sources`, `snapshot_list`, `snapshot_jobs`,
  `snapshot_download` (destructive, emits MCP progress notifications;
  takes an optional out-of-band `sha256` pin and returns
  `plaintext_transport` — an MCP caller has no stderr to read the
  cleartext-transport warning from, so check that field).
  Job control (`snapshot stop`, `snapshot logs`) is CLI-only.
- **knowledge** (2): `knowledge_list`, `knowledge_get`
- **build** (3): `build_list`, `build_inspect`, `build_prune`
  (destructive — dry-run by default; `confirm=true` actually deletes).
  Build *execution* is NOT exposed via MCP; call the CLI directly
  for that (see Workflow 6).
- **shadow-fork** (1): `shadow_fork_mutate` (destructive — mutates a
  halted java-tron data dir in place; see Workflow 5 for the full
  recipe).

The server also registers 4 **prompts** — pre-filled playbooks an MCP
client surfaces as slash-commands / suggested actions. Each orchestrates
the tools above into a guided multi-step workflow:

- `deploy_fullnode` — validate → preflight → plan → apply → verify a
  single node from an intent path (Workflow 1).
- `setup_private_network` — bring up a multi-node private network from a
  multi-node intent (Workflow 4).
- `diagnose_failing_node` — the Workflow 2 decision tree as a prompt
  (status → logs → health → diagnose → suggested fix).
- `recover_failed_upgrade` — roll a node back to its last-good config
  after a bad apply.

Prompts are scaffolding, not new capability — they wrap the same tools,
so an agent that prefers raw tool calls can ignore them.

Destructive tools carry the MCP `destructiveHint` annotation so MCP
clients prompt the user. The server's `Instructions` field
auto-injects guidance about the canonical workflows so the LLM picks
up AGENTS.md context without the user having to paste it.

## Recipes — pre-built multi-step playbooks

Five canonical multi-step workflows from the sections above are
codified as declarative YAML in `recipes/*.yaml` and run via:

```bash
trond recipe list                                   # everything available
trond recipe show <name>                            # YAML + params + steps
trond recipe run <name> --param key=value [...]     # execute end-to-end
trond recipe run <name> ... --dry-run               # print resolved chain, no exec
trond recipe run <name> ... --resume-from <step>    # skip ahead after a partial run
```

The runner re-execs the trond binary itself for each step (no shell
dependency beyond `exec`), captures stdout JSON, and feeds named
fields forward via `{{ steps.<id>.<field> }}` substitution. Steps
declare `on_failure: abort | continue | rollback`; rollback steps
run as a best-effort cleanup pass when triggered.

The output of `recipe list / show / run` is documented as JSON Schema
(`schemas/output/recipe-{list,show,run}.schema.json`); pull via
`trond schema "recipe run" --output-only -o json` or validate parsed
output against the embedded schema.

Shipped recipes:

| Name | What it does |
|---|---|
| `nile-test-fullnode` | validate → preflight → apply --wait → verify (4 steps; smoke-test workflow) |
| `fresh-mainnet-fullnode-with-snapshot` | validate → preflight → snapshot download → apply --wait → verify, with rollback to stop the failed node (5 steps) |
| `upgrade-with-verify` | snapshot status → upgrade → verify, with auto-rollback on verify failure (3 steps + 1 rollback) |
| `recover-failed-upgrade` | diagnose → rollback → status, for post-upgrade triage (3 steps) |
| `destroy-private-network-cleanly` | network status → network destroy with confirm gate (2 steps) |

For an MCP-driven agent, "run a recipe" is a single tool call that
encapsulates 3–5 underlying tool calls and the failure / rollback
state machine. For a CI pipeline, it replaces a 30-line bash script
with one declarative file. For a human at a terminal, it documents
the canonical workflow as runnable code.

---

## Versioning of this contract

Every JSON output that carries a `schema_version` field follows
semantic versioning at the field level: existing fields are stable
within a major version; additions are minor; renames or removals are
major. This file is updated alongside any contract change. If you
write an agent against trond X.Y, pin to that version and re-test on
upgrade.

If a JSON output's shape doesn't match what's documented here, that's
a bug — file an issue with the actual output and the trond version
(`trond version -o json`).

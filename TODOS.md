# TODOS

Deferred work captured during reviews. Each item carries enough context to pick
up cold.

## Open audit follow-ups

- **AUD-009 replay block-failure threshold** — aborting replay when any
  transaction broadcast in a block fails is intentional, not a bug: the cursor
  must never advance past a partially-failed block, or silently dropped
  transactions can make the shadow chain diverge from mainnet. Retry resumes
  at the failed block boundary; already-landed transactions are re-broadcast
  as expected, pinned by `tools/replay/replayer_test.go` (`want 4` assertion).
  Defer a configurable `--block-fail-threshold N` flag (default `0` = current
  strict abort; `N > 0` tolerates ≤N failures per block, while all failures
  remain in `failLog` and are reported in the summary) to a follow-up PR if
  needed.
- **AUD-020 transport-pool leak** — retire target-aware transports from the
  shared pool on every lifecycle path (`internal/target/target.go:38-48,86-94`).
- **Windows support** — complete platform-specific lifecycle and filesystem
  validation beyond the best-effort directory fsync (`internal/state/store.go`).

## `snapshot clone --from-node` for SSH-target / named-volume nodes

- **What:** `snapshot clone --from-node <name>` now resolves jar + docker
  **bind-mount** nodes on a **local** target (shipped). Two cases remain
  refused and could be supported later:
  1. **SSH-target nodes** — `fsclone.CloneDir` is local-filesystem code
     (`os.Stat`/`sameFilesystem`/local disk-free), so a remote node's path
     can't be cloned locally. Supporting it needs remote-side execution (run
     the clone over `target.Exec`, or rsync the result back).
  2. **Docker default named-volume nodes** — the chain DB lives in
     docker-managed storage (inside the VM on macOS), not a host path, so
     CoW can't fire. Would need `docker cp` extraction to a host dir first (a
     full GB-scale copy, defeating the CoW point) — dubious value.
- **Why:** completeness for rigs that don't use local bind-mount storage.
- **Context:** the local jar + docker-bind path shipped 2026-06-18; these two
  were deliberately refused (with guidance) per the `/plan-eng-review`
  (Codex caught the SSH gap — `fsclone` is local-only). The resolver +
  `dockerInspect` seam live in `cmd/snapshot/clone.go`.
- **Depends on / blocked by:** a remote clone primitive (for SSH); nothing for
  the named-volume case beyond deciding `docker cp` is worth it.

## Reconcile `scripts/db_cp.sh` with `trond snapshot clone`

- **What:** There are now TWO "fast local chain-DB copy" mechanisms: the
  Go `trond snapshot clone` verb (copy-on-write via APFS clonefile / Linux
  FICLONE, byte-copy fallback) added in #192, and `scripts/db_cp.sh`
  (rsync for metadata + hard-link for `.sst`) added in #194 (bladehan1).
  Pick one story and converge.
- **Why:** two overlapping capabilities drift and confuse — an agent/operator
  shouldn't have to guess which to use for a warm-pool fixture.
- **Options:** (a) teach `snapshot clone` a hard-link tier (between CoW and
  full byte copy) so `.sst` files share inodes when CoW is unavailable, then
  retire `db_cp.sh`; or (b) have `db_cp.sh` shell out to `trond snapshot
  clone`; or (c) explicitly document them as distinct (CoW clone vs
  hard-link backup) if the use cases really differ.
- **Context:** flagged 2026-06-17 after #194 merged. `internal/fsclone`
  owns the CoW primitive; `db_cp.sh` is a standalone shell script referenced
  in `knowledge/snapshots.md`. Hard-links and CoW have different semantics
  (hard-link shares the inode → a later in-place write to one copy corrupts
  the other; CoW is independent), so reconciling needs care, not a blind
  merge.
- **Depends on / blocked by:** nothing; both already merged.

## Emit a flag-aware `chain_id` in `status` (TVM CHAINID match)

- **What:** `status -o json` now emits `genesis_block_id` (the block-0 TRON
  block id — see `internal/apply/probe.go`). The original ask also wanted a
  `chain_id` that matches what on-chain contracts see via the TVM `CHAINID`
  opcode. That was deferred because the derivation is **flag-dependent**.
- **The verified semantics (don't re-derive wrong):** java-tron
  `Program.getChainId()` starts from `getBlockByNum(0).getBlockId()` and
  returns the **full 32-byte block id**, truncating to the **last 4 bytes
  ONLY** when `allowTvmCompatibleEvm` OR `allowOptimizedReturnValueOfChainId`
  is active. Both are off by default on a fresh private net. So a naive
  "last 4 bytes" `chain_id` is wrong for the default rig.
- **How to do it right:** probe `getchainparameters` for those two flags,
  then derive `chain_id` = full `genesis_block_id` (flags off) or its last 4
  bytes (flags on). Ideally verify once against a deployed contract reading
  CHAINID. Surface in `status`; bump SchemaVersion (additive → PATCH).
- **Cons / why deferred:** marginal value (TRON tx signing uses
  `ref_block_hash`, not a chain id; `genesis_block_id` already distinguishes
  rigs), plus the flag-probe + verification is more than a one-field add.
- **Context:** deferred during the 2026-06-18 `/plan-eng-review`; the
  outside-voice (Codex, web-verified) caught the flag-dependency that would
  have shipped a wrong `chain_id`. `genesis_block_id` shipped as the
  unambiguous anchor.

## Rename `network-create`'s `network` output field

- **What:** `network create -o json` returns `network` = the network's intent
  NAME, while `apply`/`status`/`list`/`inspect` return `network` = the chain
  kind (`mainnet|nile|private`, matching the intent's `network:` field). Rename
  network-create's field (e.g. → `name` / `network_name`) so `network` means
  one thing across the CLI.
- **Why:** an agent that learned `network` from one command misreads it in
  another. C1 widened the collision by adding `network`=chain-kind to
  apply/status.
- **Cons:** breaking change to network-create.schema.json — needs a major
  schema bump + agent migration, so it can't ride along in a feature PR.
- **Context:** flagged by the 2026-06 eng-review of the C1 private-net PR
  (Issue 4 / Codex #7). Documented in AGENTS.md's safety section meanwhile.
- **Depends on:** a deliberate schema-version major bump.

## Jar remove should scrub retained witness key

- **What:** `trond remove` without purge (`--keep-data`) may leave an inline
  witness key in `config.conf` under the install directory; assess default
  scrubbing or document the retained secret clearly.
- **Context:** Council R2 skeptic.

## Preserve change tracking on remove read errors

- **What:** `changeTracker.remove` treats non-NotExist `ReadFile` errors
  (SSH/permission transient) as absent; a successful `rm` then leaves
  `changed` false, so a running JVM retains old env until restart. Distinguish
  errors or conservatively set `changed`, and add read-error+rm-success
  coverage.
- **Priority:** P2.
- **Context:** Council R2 skeptic.

## Reject control characters in systemd drop-in Environment values

- **What:** Reject control characters in drop-in `Environment=` values; newline
  or `#` could theoretically inject extra systemd directives.
- **Priority:** P3; operator-self threat model, low risk.
- **Context:** Council R2 implementer.

## Add real systemd lifecycle E2E coverage

- **What:** Real systemd E2E for `daemon-reload`, `enable`, `is-active`, and
  journal semantics is missing; add a systemd runner or formally declare the
  support boundary.
- **Status:** UNVERIFIED.
- **Context:** Council R1/R2.

## Define state-save failure reconciliation

- **What:** Define recovery and consistency semantics when runtime mutation
  succeeds but state save fails, including a reconcile strategy.
- **Context:** Council R1 skeptic,洞#8.

## Expand schema producer/consumer contract coverage

- **What:** Output schema↔producer bidirectional assertions currently cover
  only apply/events/status; extend them to every output schema. Also declare
  `intent_hash` in `inspect.schema.json`.
- **Context:** Council R2 architect; long-term debt.

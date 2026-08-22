# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The agent-ergonomics arc lands across four sequenced PRs:
**#151** (CLI core + AGENTS.md) → **#152** (`trond schema`) →
**#153** (`trond mcp`) → **#154** (`trond recipe`).

### Added
- **`jvm.extra_opts`** — an escape hatch for JVM flags outside the closed
  heap/GC field set, appended last so they win on any last-flag-wins
  option. Needed because trond runs the JAR directly and so never reads
  java-tron's `gradle/java-tron.vmoptions`, which its distribution launcher
  (`bin/FullNode`) does: `-Dio.netty.allocator.type=pooled` is the live
  example — java-tron sets it to opt out of netty 4.2's adaptive allocator,
  and without the hatch a trond-deployed node silently runs on the
  allocator upstream deliberately avoided. Restricted to `-D<key>=<value>`
  and `-XX:…`; whitespace and quoting characters are refused, so one entry
  is always exactly one argument.
- `apply.Options.SkipMonitoring` suppresses the per-node monitoring stack
  while leaving `Intent.Monitoring` intact for rendering. `network create`
  needs both halves: `RenderHOCON` keys its metrics auto-enable off the
  field, but the network owns one stack scraping every node — without the
  flag each node deployed its own Prometheus and the last one won, leaving
  a stack that looks healthy while observing a fraction of the network.
- **Agent integration arc (ai-ops): machine-observable, provably-private rigs.**
  - (#190/#193/#196) **Private-net safety gate (C1).** `is_private` is a
    queryable fact in `status`/`list`/`inspect`. A persistent
    `--require-private` flag + `TROND_REQUIRE_PRIVATE` env make trond refuse
    to mutate any non-private node (`PRIVATE_NETWORK_REQUIRED`, exit 2) —
    across `apply`, `network create/add/destroy/upgrade`, every per-node
    mutator (`start`/`stop`/`restart`/`remove`/`rollback`/`upgrade`), the
    chaos primitives, and the MCP `apply` tool. One-way floor (env can't be
    overridden); enforced on recorded state before any target resolution.
  - (#191) **Machine-observable rig state (A1).** `status`/`inspect -o json`
    gain `healthy` (RPC-liveness, real-block gated), `container_id`, and a
    runtime-discriminated `logs` locator (docker exec path / journald unit).
  - (#195) **Build identity (B1).** `status`/`inspect` surface
    `build_cache_key` + a clean `build_revision` (the java-tron commit a
    source-built node runs).
  - (#197) **`genesis_block_id` (A1).** `status` emits the block-0 TRON block
    id — the chain's identity fingerprint — from a live probe.
  - (#192/#198) **Warm-pool clones (B3).** `trond snapshot clone <src> <dst>`
    copy-on-write clones a chain-DB dir (APFS clonefile / Linux FICLONE,
    byte-copy fallback); `--from-node <name>` resolves a stopped local node's
    DB dir from state (jar + docker bind-mount).
  - (#182) Nile snapshot source URL refreshed + a weekly source-probe.
  - (#199) `inspect -o json` surfaces the `monitoring` stack
    (`enabled` / `prometheus_port` / `grafana_port`) for `--monitor` nodes;
    `ManagedNode` state tracks `metrics_port` so `network add` rebuilds
    Prometheus scrape targets with the node's real metrics port instead of a
    hardcoded 9527. (Also bundled a txgen Falcon liboqs resource-leak fix.)
- (#164/#165/#166) Shadow-fork release-gate fixes empirically validated
  on amd64 + qemu-arm64 EC2 (2026-05-25/26):
  - `internal/dbfork/db/leveldb.go` Close() now sweeps `.ldb → .sst`
    and removes `.bak`/`.old` residue so java-tron leveldbjni 1.8 +
    tronprotocol leveldbjni-all 1.18.2 can read back what dbfork
    wrote. **Task #164.**
  - `internal/render/hocon.go` wires `ports.jsonrpc` + `ports.metrics`
    into HOCON's `httpFullNodePort` and `prometheus.port`. Previously
    `features.jsonrpc=true` enabled the service but left the port
    commented at the template default, breaking docker→java-tron
    port mapping. **Task #165.**
  - `go.mod` pins `linxGnu/grocksdb` to v1.9.7 (RocksDB 9.7.3) so
    dbfork's MANIFEST writes are version-compatible with java-tron
    arm64's rocksdbjni 9.7.4. v1.10.x produced VersionEdit unknown-
    tag crashes at AccountStore init. **Task #166.** Operators
    rebuilding `-tags rocksdb` need a fresh `make libs` against the
    new pin.
- (#161) Nile snapshot mirror table refreshed —
  `nile-snapshots.s3-accelerate.amazonaws.com` (403 since at least
  2026-Q1) replaced by `snapshots.nileex.io`. New
  `EngineRocksDB` row for the `/rocksdb` prefix so arm64 operators
  can `trond snapshot download --network nile --type full --db-engine
  rocksdb`. The structural follow-up is task #161-cron in the
  `feat/snapshot-sources-probe` branch.
- (#163-followup) `.github/workflows/dbfork-equivalence.yml` runs
  `TestEquivalence_GoVsJava` on every PR touching `internal/dbfork`
  + weekly cron + manual dispatch. Builds the java toolkit's
  shadowJar on demand and caches a Nile fixture week-to-week. This
  is the byte-for-byte Go-vs-Java release gate.
- (#156) JSON Schemas for `trond recipe list / show / run` outputs
  (`schemas/output/recipe-{list,show,run}.schema.json`). Closes the
  schema-coverage gap left by #154 — `trond schema "recipe run"
  --output-only -o json` now returns the canonical RunResult shape
  instead of empty. Bumps `SchemaVersion` to `1.1.0` (minor: new
  schemas added, no existing schema modified).
- (#154) `trond recipe list / show / run` — declarative multi-step
  workflow runner. Recipes are YAML files in `recipes/*.yaml` (also
  embedded via go:embed) that codify the canonical AGENTS.md
  workflows: deploy fresh node, deploy with snapshot, upgrade with
  auto-rollback, recover failed upgrade, destroy private network.
  The runner re-execs the trond binary for each step, captures JSON
  output, and feeds named fields forward via
  `{{ steps.<id>.<field> }}` substitution. Per-step `on_failure:
  abort | continue | rollback` controls failure semantics; rollback
  steps run as best-effort cleanup. Five recipes ship; adding more
  is one YAML file each.
- (#153) `trond mcp` — Model Context Protocol server. Speaks JSON-RPC over
  stdio so chat-based / IDE-embedded agents (Claude Desktop, Cursor,
  Cline, Continue.dev, Zed AI, ChatGPT Apps) can call trond
  capabilities as structured tools without shelling out. Registers
  16 tools across inspection (list/status/inspect), diagnostic
  (doctor/version/health/diagnose), config (config_validate /
  config_render / plan), lifecycle (apply, marked destructive),
  snapshot (sources/list/jobs/download with MCP progress
  notifications, marked destructive), and knowledge (list/get).
  Tool input/output schemas auto-derived from typed Go structs via
  the SDK's struct-tag inspection, kept in lockstep with the
  implementations. Server Instructions field injects AGENTS.md-style
  workflow guidance so the LLM picks up context without the user
  pasting it.
- (#152) `trond schema [command]` — dumps the entire CLI surface as a JSON
  manifest: every command, flag, type, default, plus the JSON Schema
  for each command's `--output json` result. Top-level
  `schema_version` for clients to pin against. With a positional arg
  the dump narrows to that command; with `--output-only` to just its
  output schema, suitable for piping into a JSON Schema validator.
- (#152) 21 JSON Schema files under `schemas/output/*.schema.json` covering
  every command that supports `-o json`: apply, plan, status, list,
  inspect, diagnose, health, verify, preflight, doctor, version,
  events, config validate/render, network create/status,
  snapshot sources/list/download/jobs, plus a shared error envelope.
  All draft-2020-12; canonical `$id` URLs match repo paths so offline
  validators resolve $refs without network.
- (#151) `AGENTS.md` at repo root — machine-readable contract for AI agents
  that CALL trond (distinct from `CLAUDE.md` which targets agents
  EDITING this repo). Covers the JSON output convention, exit-code
  semantics with retry strategy per code, four core workflows
  (deploy / diagnose / snapshot / private network) with command
  chains and field-level expectations, concurrency isolation via
  `TROND_STATE_DIR`, anti-patterns. Linked from README and CLAUDE.md.
- (#151) Release signatures via Sigstore cosign keyless OIDC. `checksums.txt`
  is signed at release time using the GitHub Actions workflow's
  short-lived OIDC token; the resulting `.sig` and `.pem` ship as
  release artifacts and the signing event is recorded in Rekor. No
  long-lived signing key is stored anywhere. Verification documented
  in README ("Verifying a release") and `trond knowledge
  release-signatures` (long-form: what keyless OIDC proves vs.
  doesn't, alternatives table, SLSA upgrade path, common errors).
- (#151) `trond snapshot` — chain-database snapshot subsystem (mainnet ×6 + nile
  mirrors). Streams the upstream `.tgz` through gunzip + tar in one
  pipeline, never writing the archive to disk. HEAD probe + `Statfs` verify
  free space before any GET; existing `output-directory/database` refuses
  overwrite without `--force` (HUMAN_REQUIRED, exit 10); pre-existing
  `userdata/` is preserved across extraction. MD5 sidecar verified inline.
  Subcommands:
    - `snapshot sources` — list mirrors (text or JSON)
    - `snapshot list --network mainnet|nile [--domain ...]` — available backups, newest-first
    - `snapshot download --network <n> [--type lite|full] [--region s|a]
      [--db-engine leveldb|rocksdb] [--backup ...] [--to <dir>]
      [--node <name>] [--force] [--no-verify] [--dry-run] [--detach]`
    - `snapshot jobs` — list background downloads (running / stopped)
    - `snapshot logs <job-id> [-f] [-n N]` — tail / follow log
    - `snapshot stop <job-id> [--force]` — SIGTERM (or SIGKILL)
- `--detach` re-execs trond with `SysProcAttr.Setsid=true`; the child
  becomes PPID 1 and survives terminal close. Logs land at
  `~/.trond/snapshots/<id>.log`; manifests at `<id>.json`.
- `trond doctor` — environment self-check (state, lock, docker CLI,
  version drift via `--check-update`)
- `trond version --check-update` — query GitHub releases, compare to
  the local build
- `trond completion --install` — drop the script in the per-shell
  standard location
- `config render --node N` — render only one node from a multi-node intent
- `config render --overlay <path>` — second intent merged on top
- `config render -o json` — structured payload (hocon, compose, systemd,
  jvm_args per node)
- `config validate --explain` — per-field breakdown of explicit vs
  default values, with derived JVM heap shown
- `list --label k=v` and `inspect --label k=v` — repeatable AND filters
  scoped to docker labels persisted in state
- `nodes[].jar.{url,sha256}` — declarative jar download, https-only,
  SHA256 mandatory when url is set
- README: "Repository Evolution" + "Intent Reference" sections, install
  one-liner, brew/deb/rpm/docker routes, shell completion section,
  Chinese mirror in `README_CN.md`
- `LICENSE` (MIT), `CHANGELOG.md`, `CONTRIBUTING.md`,
  `.github/{ISSUE_TEMPLATE,PULL_REQUEST_TEMPLATE}.md`,
  `.github/dependabot.yml`
- `cmd/gendoc` — emits man(1) pages and per-command markdown
- `scripts/install.sh` — single-shot installer with SHA256 verification

### Changed
- `goreleaser` now produces .deb / .rpm / .apk packages alongside the
  tar.gz archives; release notes group commits by feat/fix
- CI matrix expanded: `lint`, `test+coverage`, `govulncheck`, and
  cross-compile jobs run on every PR; e2e is its own workflow with
  the heavier schedule
- `config explain` renamed to `config docs`; `explain` kept as alias
- `auto_ports` now verifies TCP+UDP availability before allocating
  (fixes P2P UDP-only collisions on macOS Docker)
- `network destroy` resolves target per-node from state (was hard-coded
  LocalTarget — SSH-deployed networks would leak containers)
- `network add` persists the full target (Host/User/Port/IdentityFile/
  InstallPath/Labels) so subsequent commands can rebuild the SSH
  connection
- `apply` short-circuits on hash match regardless of node status (was
  forcing HUMAN_REQUIRED on a stopped node with an unchanged intent)
- jar runtime `Remove(purge=true)` actually wipes the install dir
  (was a documented TODO before); refuses `/` and empty paths

### Fixed
- **`config_overrides` rendered Go syntax, not HOCON.** `hoconValue` fell
  back to `fmt.%v` for slices and maps, emitting `[map[address:T… voteCount:5000]]`
  — which no HOCON parser accepts — so every list-valued override was
  unusable and a multi-witness `genesis.block.witnesses` (the two-SR private
  net tron-docker documents) could not be expressed in an intent at all. The
  same function used `fmt.%q` for strings, which emits Go's `\x01` for a
  control byte rather than JSON's `\u0001`. Both now render through a JSON
  encoder (HOCON is a JSON superset) with HTML escaping off so URLs survive
  verbatim. Numbers deliberately stay on `fmt` — `%v` is already
  JSON-compatible there and routing them through the encoder would change
  every rendered config for no correctness gain.
- **`build.revision` labelled the artifact without building it.** The git
  worktree checkout ran only when `build.patches` was non-empty, so an
  explicit branch/tag/sha compiled whatever the working tree happened to
  hold and then stamped the artifact — cache key, manifest, and
  `status.build_revision` — with the revision that was asked for. Two
  `trond build --revision <ref>` runs against different refs could hand back
  byte-identical artifacts under different labels, silently reducing a
  base-vs-head comparison to comparing an artifact with itself. The
  worktree now runs whenever an explicit non-HEAD revision is requested.
  `revision: HEAD` still builds the working tree, dirty edits included —
  that is the dev inner loop, and the dirty state is already folded into
  the cache key.
- **`network create` bypassed `internal/apply.Apply`.** Its hand-rolled
  render + deploy + state loop had drifted from the core in three ways: it
  never called `internal/build`, so a node declaring `build:` rendered an
  empty `image:` and deployed nothing usable — no error, no warning, and a
  green `config validate`; it hardcoded JDK 17 for JVM arg selection
  instead of probing the target; and it hardcoded the docker runtime
  instead of honouring `target.runtime`. Every node now goes through
  `Apply`, projected to a single-node intent with its own name and hash so
  idempotency stays per node.
- **`Apply` did not persist `P2PPort`.** `network add` builds a joining
  node's peer list from the `P2PPort` of every node in state and skips any
  entry where it is zero, so a node deployed through `Apply` was invisible
  as a peer: the late joiner came up with an empty peer list, never
  connected, and neither command said why.
- Witness private key inlined into rendered HOCON — typesafe-config does
  not perform `${ENV}` substitution, the literal `${SR_KEY}` was being
  read as a 9-char witness key and the SR shut down with WITNESS_INIT(1)
- Compose render aligned with the official tronprotocol/java-tron image:
  `/java-tron/conf`, `/java-tron/output-directory`, `/java-tron/logs`,
  `-jvm` arg, P2P UDP exposed. Containers no longer crash-loop
- `network create` auto-wires `node.active` between siblings so peering
  works when `auto_ports` randomizes the inside-container P2P port
- `network destroy --confirm=typo` now refuses with NETWORK_NOT_FOUND
  (previously silently succeeded with `removed: null`)
- `port_listening` checker uses net.Dial instead of `ss -tlnp` (works
  on macOS where ss isn't available)
- `trond logs` reads `/java-tron/logs/tron.log` (java-tron writes to
  file, not stdout — `docker compose logs` was empty)
- `state.NodeTarget` persists `IdentityFile` (lifecycle commands lost
  the SSH key after apply)

### Security
- **GitHub Releases is the only publication channel.** The Homebrew tap and
  the `tronprotocol/trond` Docker image were configured and are removed
  before the first tag. Both needed a long-lived credential in this
  repository's Actions secrets — a cross-repo PAT for the tap (the default
  `GITHUB_TOKEN` cannot write to another repository) and a `DOCKERHUB_TOKEN`
  — and either would let its holder publish under this project's name until
  someone noticed and rotated it. That is the standing-secret risk cosign
  keyless signing exists to remove. Neither channel's artifact could be
  listed in `checksums.txt` either, so the README's claim that the signature
  covered "docker images, the homebrew formula" was false; a snapshot build
  confirms `checksums.txt` holds only the archives and the deb/rpm/apk
  packages. README, the goreleaser manifest, and the project constitution
  now agree on this
- Third-party actions in `release.yml` are pinned to a commit SHA rather
  than a moving tag, and `goreleaser-action`'s `version: latest` is pinned
  to a range. That job holds `contents: write` and `id-token: write`, so
  everything it runs is inside the trusted computing base of every
  signature the project issues; a tag repointed upstream would have been an
  unreviewed change to the signer
- The goreleaser `before:` hook ran `go mod tidy`, which WRITES to
  `go.mod`/`go.sum` — a release could be built from a lockfile other than
  the reviewed, committed one (a local snapshot run did modify `go.sum`).
  Replaced with `go mod download` + `go mod verify`
- Reject control characters (`\n`, `\r`, …) in every free-form intent
  field; struct-tag `safe_string` + manual map/slice walk close compose
  YAML and systemd unit injection vectors
- `nodes[].jar.url` rejects http/file/ftp; SHA256 required when URL set
- SSH command whitelist trimmed to lifecycle minimum (drop apt-get / yum
  / dnf / curl / wget / kill / pkill / chown / cp / mv); `quoteArgs`
  now also shellQuotes the cmd token defensively
- SSH host-key verification distinguishes new host (eligible for opt-in
  TOFU via `TROND_SSH_ACCEPT_NEW_HOSTS=1`) from pinned-key MISMATCH
  (always rejected, even with TOFU)
- `network add` now honors target.type instead of hard-coding
  LocalTarget (an SSH intent would otherwise deploy on the operator's
  local host)
- **Snapshot downloads: the cleartext transport is now visible, and a
  strong digest can be pinned.** The six mainnet mirrors are bare IPs
  that publish no HTTPS endpoint (probed 2026-07-31: `:443` closed,
  `:80` answers), so their tarballs — and the `.md5sum` sidecar, which
  rides the same connection — are unauthenticated in transit; a matching
  MD5 has therefore never attested provenance, only that the transfer
  was not corrupted. trond no longer lets that pass silently:
  `snapshot download` warns on stderr before any bytes move (under
  `--detach` the warning lands in the job log), reports
  `plaintext_transport` in `-o json` on both the `--dry-run` preflight
  and the completion payload and via the MCP `snapshot_download` tool,
  and shows a `transport:` row in the dry-run table. A new `--sha256
  <hex>` flag (and matching MCP `sha256` arg) checks an out-of-band
  digest — the only verification in this flow that can detect a
  substituted archive; a mismatch fails the download and a malformed pin
  is rejected before the transfer starts. The SHA-256 of every download
  is computed and reported regardless, so an operator can pin it on
  later fetches of the same backup. The mainnet `BaseURL`s are
  deliberately *not* rewritten to `https://` — those mirrors have no TLS
  to upgrade to, and pretending otherwise would break every mainnet
  download. Existing MD5 behaviour, verification ordering, and disk
  headroom are unchanged. Schema `1.12.2` → `1.12.3` (additive optional
  fields on one schema).
- `snapshot download` fails closed on integrity: the `.md5sum` sidecar is
  always fetched (a preflight HEAD no longer decides whether to verify),
  and a 404 / transport failure aborts with `VERIFICATION_UNAVAILABLE`
  (exit 1) instead of silently extracting an unverified chain database.
  `--no-verify` (MCP `snapshot_download` gains `no_verify`, previously
  absent) is the only way to skip the check; results carry
  `verification_skipped` so `md5_verified: false` can no longer be read
  as "the mirror had no sidecar". Schema 1.12.2 → 1.12.3
- **Breaking:** the rendered monitoring stack binds Grafana's host port
  to `127.0.0.1` instead of `0.0.0.0`; it previously published the
  grafana-oss default `admin/admin` login to every network that could
  reach the deployment host. Use an SSH tunnel, or opt back in with
  `monitoring.grafana.expose: true`, which now requires
  `monitoring.grafana.admin_password_env` (the NAME of an env var
  feeding `GF_SECURITY_ADMIN_PASSWORD`)

## [0.1.0-alpha] — 2026-XX-XX

Initial public alpha. The project transitions from a curated set of HOCON
configuration templates into a CLI for declarative TRON node deployment.

### Added
- 32 CLI commands across lifecycle (apply / stop / start / restart / upgrade /
  rollback / remove), configuration (validate / render / diff / docs),
  observability (status / list / logs / health / diagnose / verify / inspect /
  events), test-harness SDK (exec / files / wait), chaos primitives
  (disconnect / connect / partition / heal), private networks (create / add /
  status / destroy), environment (preflight / bootstrap), knowledge base, and
  meta (version / completion / help)
- Declarative intent.yaml schema covering ~50 fields:
  target (local/ssh, runtime, auto_ports), node (type, version, image, ports,
  resources, jvm, storage, restart, extra_env, extra_args, labels, networks,
  depends_on, healthcheck, ulimits, extra_hosts, entrypoint, logging,
  shm_size, jar source URL+SHA256), network_overrides
  (seeds, active_peers, p2p_version, discovery, max_connections, …),
  witness_key (private_key_env, keystore_path, account_address),
  config_overrides (arbitrary HOCON dotted-key escape hatch)
- HOCON two-pass render: in-place key rewrites + appended override block
- Compose render aligned with the official `tronprotocol/java-tron` image:
  `/java-tron/conf`, `/java-tron/output-directory`, `/java-tron/logs`
- SSH host-key verification with explicit MITM detection (TOFU opt-in via
  `TROND_SSH_ACCEPT_NEW_HOSTS=1`)
- SSH command whitelist enforced at `Exec` entry
- Private key never written to env or stdout — `PrivateKey` type redacted in
  every formatter; witness key inlined into HOCON (which is 0600 on disk)
- `--state-dir` / `TROND_STATE_DIR` for parallel test enclaves
- `target.auto_ports: true` allocates free TCP+UDP ports automatically
- `network create` auto-wires `node.active` peering between siblings
- Audit log (JSONL) for every mutating command, streamable via `events --follow`

### Repository changes
- HOCON templates remain at the repository root (`main_net_config.conf`,
  `test_net_config.conf`, `private_net_config.conf`) and continue to track
  upstream. `make sync-templates` refreshes them
- Embedded copies under `internal/render/templates/` are kept in sync at
  release time and bundled into the binary so `trond config render` works
  from any working directory

[Unreleased]: https://github.com/tronprotocol/tron-deployment/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/tronprotocol/tron-deployment/releases/tag/v0.1.0-alpha

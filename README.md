# trond - TRON Node Deployment CLI

A command-line tool for deploying, managing, and diagnosing [java-tron](https://github.com/tronprotocol/java-tron) nodes using declarative intent files.

> **Heads-up for prior users of this repository.** Until recently this repo
> shipped only the three java-tron HOCON templates (`main_net_config.conf`,
> `test_net_config.conf`, `private_net_config.conf`) for users to copy and
> edit by hand. Those files are **still here, still authoritative, still
> synchronised with upstream** (see [Configuration Templates](#configuration-templates)
> below). What's new is `trond`, a CLI that consumes those same templates
> and a small declarative `intent.yaml` to render, deploy, and manage nodes
> end-to-end. If you only need the raw `.conf` files, nothing has changed —
> they live where they always did. If you want to skip hand-editing them,
> read on.

## Features

- **Declarative deployment** -- describe desired state in YAML, `trond` makes it happen
- **Multi-runtime** -- Docker Compose or native Jar + systemd
- **Multi-target** -- local host or remote SSH
- **Idempotent** -- `trond apply` is safe to run repeatedly
- **Plan/Apply workflow** -- preview changes before deploying
- **Structured output** -- JSON output for CI pipelines and AI agents
- **Agent-observable state** -- `status` / `inspect` expose `healthy`, `container_id`, a runtime log locator, build revision, and `genesis_block_id` so an agent never has to screen-scrape or guess
- **Private-net safety gate** -- `--require-private` (or the `TROND_REQUIRE_PRIVATE` env) makes trond *refuse* to mutate any non-private node; `is_private` is a queryable fact
- **Warm-pool clones** -- `trond snapshot clone` copy-on-write clones a chain DB into an isolated fixture in seconds
- **Built-in diagnostics** -- sync health, peer count, disk, ports, version checks
- **Built-in monitoring** -- optional Prometheus + Grafana stack with pre-built dashboards
- **Knowledge base** -- embedded deployment guidance and troubleshooting

## Install

### One-liner

```bash
curl -fsSL https://raw.githubusercontent.com/tronprotocol/tron-deployment/master/scripts/install.sh | sh
```

The script downloads the latest release for your OS / arch, verifies the
SHA256, and drops the binary into `/usr/local/bin` (or `~/.local/bin` if it
can't write the system path). Set `TROND_VERSION=v0.x.y` to pin a specific
release, or `TROND_DEST=/path` to install elsewhere.

### Homebrew (macOS / Linuxbrew)

```bash
brew install tronprotocol/tap/trond
```

### Linux packages

`.deb`, `.rpm`, and `.apk` packages are attached to every release, e.g.

```bash
# Debian / Ubuntu
curl -LO https://github.com/tronprotocol/tron-deployment/releases/latest/download/trond_VERSION_linux_amd64.deb
sudo dpkg -i trond_*.deb

# RHEL / Fedora
sudo rpm -i https://github.com/tronprotocol/tron-deployment/releases/latest/download/trond_VERSION_linux_amd64.rpm
```

### Docker

```bash
docker run --rm -v ~/.trond:/home/trond/.trond \
  -v /var/run/docker.sock:/var/run/docker.sock \
  tronprotocol/trond:latest --help
```

### From release tarball

Download from [Releases](https://github.com/tronprotocol/tron-deployment/releases):

```bash
curl -LO https://github.com/tronprotocol/tron-deployment/releases/latest/download/trond_VERSION_linux_amd64.tar.gz
tar xzf trond_VERSION_linux_amd64.tar.gz
sudo mv trond /usr/local/bin/
```

### From source

`make` downloads a pinned Go toolchain into `./.go-toolchain/<version>/`,
caches modules under `./.gopath/`, and builds with that — nothing
leaks into `~/go` or your system Go install. A fresh clone produces
an identical binary on any host with `bash`, `curl`/`wget`, and `tar`.

```bash
git clone https://github.com/tronprotocol/tron-deployment.git
cd tron-deployment
make build
# Binary: ./bin/trond
```

The first build downloads roughly 250 MB total (~150 MB Go toolchain,
~100 MB module cache) and takes ~15 s on a typical link; subsequent
builds reuse the cached toolchain (1–2 s).

If you'd rather use the Go you already have, opt out:

```bash
make USE_SYSTEM_GO=1 build
```

Reclaim every byte the build downloaded:

```bash
make clean-all          # removes bin/, .go-toolchain/, .gopath/
```

### Shell completion

```bash
# Print to stdout (pipe to wherever your shell loads from)
trond completion bash
trond completion zsh
trond completion fish

# Or install to the per-user default location
trond completion --install bash    # → ~/.local/share/bash-completion/completions/trond
trond completion --install zsh     # → ~/.config/zsh/completions/_trond
trond completion --install fish    # → ~/.config/fish/completions/trond.fish
```

For zsh you'll also want this in `~/.zshrc` (one-time):

```zsh
fpath=(~/.config/zsh/completions $fpath)
autoload -U compinit && compinit
```

## Verifying a release

Every published release is signed with [Sigstore](https://www.sigstore.dev/) `cosign` keyless OIDC — no long-lived signing key, identity is the GitHub Actions workflow that built the release. The signature covers `checksums.txt`, which transitively covers every artifact (tarballs, .deb / .rpm / .apk packages, docker images, the homebrew formula).

One-time setup:

```bash
# Linux / macOS — single static binary, no daemon
go install github.com/sigstore/cosign/v2/cmd/cosign@latest
# or via Homebrew
brew install cosign
```

Verify a tarball:

```bash
TAG=v0.1.0    # the release you downloaded
BASE=https://github.com/tronprotocol/tron-deployment/releases/download/$TAG

# Pull the three signature artifacts alongside your tarball.
curl -LO "$BASE/checksums.txt"
curl -LO "$BASE/checksums.txt.sig"
curl -LO "$BASE/checksums.txt.pem"

# Step 1: confirm the signature was issued by THIS repo's release workflow.
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature   checksums.txt.sig \
  --certificate-identity-regexp "^https://github\.com/tronprotocol/tron-deployment/\.github/workflows/release\.yml@refs/tags/" \
  --certificate-oidc-issuer     https://token.actions.githubusercontent.com \
  checksums.txt

# Step 2: now that checksums.txt is trusted, confirm your tarball matches.
sha256sum --check --ignore-missing checksums.txt   # Linux
shasum -a 256 --check --ignore-missing checksums.txt   # macOS
```

If both steps pass you have proof the artifact came from a tagged run of this repo's `release.yml`, recorded immutably in the public [Rekor transparency log](https://rekor.sigstore.dev/).

For more — what keyless OIDC actually proves, what it doesn't, when to use SLSA provenance on top — see `trond knowledge release-signatures`.

## Quickstart

1. **Create an intent file** (or use an example):

```yaml
name: my-fullnode
network: mainnet
target:
  type: local
  runtime: docker
nodes:
  - type: fullnode
    version: "4.8.1"
```

2. **Validate** the intent:

```bash
trond config validate intent.yaml
```

3. **Preview** changes:

```bash
trond plan --intent intent.yaml
```

4. **Deploy**:

```bash
trond apply --intent intent.yaml --auto-approve
```

5. **Check status**:

```bash
trond status my-fullnode
```

6. *(Optional)* **Skip the genesis sync** with a snapshot. A fresh mainnet
   fullnode otherwise spends days catching up from block 0. trond keeps
   the download and the apply step separate — apply stays seconds-fast
   and idempotent, while the download survives terminal close on its
   own.

```bash
# 1. Stage the chain database (hours, runs in background).
sudo mkdir -p /srv/tron/my-fullnode && sudo chown $USER /srv/tron/my-fullnode
trond snapshot download --network mainnet \
  --to /srv/tron/my-fullnode --detach

# 2. Walk away. Check back any time:
trond snapshot jobs
trond snapshot logs <job-id> -f       # or just `jobs`

# 3. Once `jobs` shows state=stopped (and last line is clean), deploy.
#    The intent below points storage.data at the extracted directory so
#    the node starts caught-up.
trond apply --intent examples/mainnet-fullnode-snapshot.yaml \
  --auto-approve --wait
```

Full annotated example with the storage layout: [`examples/mainnet-fullnode-snapshot.yaml`](examples/mainnet-fullnode-snapshot.yaml). Long-form rationale: `trond knowledge snapshots`.

## Dev loop — build from source

When you need a custom build (a fork, an unreleased patch, a wired-in
debugger), put a `build:` block under the node and `trond apply`
handles compile-then-deploy in one shot. Builds are content-addressed
and cached, so re-applying an unchanged source is a sub-second cache
hit.

```yaml
# my-dev-node.yaml
target: { type: local, runtime: jar }
nodes:
  - type: fullnode
    install_path: /tmp/trond-dev/my-dev-node
    build:
      source: ../java-tron              # your local checkout
      gradle_task: ":framework:buildFullNodeJar"
```

```bash
trond apply --intent my-dev-node.yaml -o json
# First run: ~5-10 min compile (longer under cross-arch QEMU).
# Re-runs against the same source: ~50 ms cache hit.
trond build list                          # what's in the cache
trond build prune --older-than 168h --confirm   # GC 7-day-old entries
```

Choose `--builder host` (uses your local `./gradlew`) for fastest
iteration, or the default `--builder docker` (pinned eclipse-temurin
image) for reproducible cross-machine builds. SSH targets build
locally and stream the JAR over with an SHA256-skip fast-path.

Full walkthrough including image artifacts, cross-arch builds,
cache management, and MCP usage: [build pipeline quickstart](specs/002-trond-build-pipeline/quickstart.md).

## Commands

### Lifecycle

| Command | Description |
|---|---|
| `trond apply` | Deploy or update a node (alias: `deploy`); supports `--wait` |
| `trond plan` | Preview changes without deploying |
| `trond status <node>` | Node state, block height, sync progress + `healthy`, `container_id`, log locator, `is_private`, `build_revision`, `genesis_block_id` (JSON) |
| `trond list` | List all managed nodes |
| `trond stop` / `start` / `restart` `<node>` | Process control |
| `trond remove <node>` | Remove a deployed node |
| `trond upgrade <node>` | Upgrade to a new version (auto-rollback on failure) |
| `trond rollback <node>` | Rollback to previous version |
| `trond logs <node>` | Stream node logs |
| `trond diagnose <node>` | Run structured health checks |
| `trond health <node>` | Quick HTTP API probe |
| `trond verify <node>` | Post-deploy health gate |
| `trond preflight` | Check target readiness |
| `trond bootstrap` | Install Docker or JDK on target |

### Build (compile java-tron from source)

| Command | Description |
|---|---|
| `trond build` | Build a JAR or docker image from a java-tron source tree (content-addressed cache) |
| `trond build list` | List cached build artifacts |
| `trond build inspect <key>` | Show full manifest for one cache entry (full key or unambiguous prefix) |
| `trond build prune` | Remove cached entries per policy (`--older-than`, `--keep-last`, `--orphan`, `--all`); dry-run unless `--confirm` |

### Configuration

| Command | Description |
|---|---|
| `trond config validate` | Validate an intent file |
| `trond config render` | Render HOCON config from intent |
| `trond config diff` | Diff rendered vs deployed config |
| `trond config docs <key>` | Look up documentation for a HOCON config key (alias: `explain`) |
| `trond config validate --explain` | Per-field intent breakdown (explicit / default / derived) |

### Networks

| Command | Description |
|---|---|
| `trond network create` | Create a multi-node private network |
| `trond network add` | Append a node to an existing network |
| `trond network status` | Show private network status |
| `trond network destroy` | Destroy a private network |

### Private-network safety (for unattended agents)

`status` / `list` / `inspect` report `is_private` (true only when `network: private`), so an agent can *prove* a rig is a throwaway private net before touching it.

Pass `--require-private` (a persistent flag on any command) or export `TROND_REQUIRE_PRIVATE=1` once, and trond **refuses to mutate a non-private node** — `PRIVATE_NETWORK_REQUIRED`, exit 2. It covers every mutating verb: `apply`, `network create/add/destroy/upgrade`, `start` / `stop` / `restart` / `remove` / `rollback` / `upgrade` / `auto-heal`, and the chaos primitives (`disconnect` / `partition` / …), plus the MCP `apply` tool's `require_private` arg and the MCP `auto_heal` tool. The gate is a one-way floor: once the env is set, `--require-private=false` cannot turn it off. Read-only verbs (`status`, `inspect`, `wait`, `diagnose`) are always allowed, as is `auto-heal --dry-run` / `auto_heal dry_run=true` (it proposes actions without taking any). Default behaviour is unchanged when neither is set.

For `apply` the gate reads both the intent's `network:` label and the recorded network of the node already deployed under that `name:` — an intent cannot re-label a mainnet/nile node `private` to get at it. That relabel is refused with `NETWORK_MISMATCH` (exit 2) even with the gate off, so a node's recorded network is never silently downgraded; `trond remove` the node first if you really mean to redeploy that name on another network.

### Snapshots

Skip the days-long sync from genesis by pulling an official chain database snapshot. trond streams the tarball through gunzip + tar in one pipeline (no on-disk `.tgz`), MD5-verifies inline, and pre-checks free disk space.

| Command | Description |
|---|---|
| `trond snapshot sources` | List known mirrors (mainnet ×6 + nile) |
| `trond snapshot list --network <net>` | Show available backups for a source |
| `trond snapshot download --network <net> [--type lite\|full] [--region <r>] [--detach]` | Stream-download into `./output-directory` (or `--to <dir>`) |
| `trond snapshot jobs` | List background download jobs (with `--detach`) |
| `trond snapshot logs <job-id> [-f] [-n N]` | Tail / follow a background download's log |
| `trond snapshot stop <job-id> [--force]` | SIGTERM (SIGKILL with `--force`) a background download |
| `trond snapshot clone <src> <dst>` | Copy-on-write clone a chain-DB dir into a fresh path (warm-pool fixtures) |
| `trond snapshot clone --from-node <name> <dst>` | Same, resolving `<src>` from a STOPPED local managed node |

`--detach` re-execs trond as a session leader (`Setsid`); the child becomes PPID 1 and survives terminal close. Logs land at `~/.trond/snapshots/<id>.log`. Existing chain data is refused without `--force` (HUMAN_REQUIRED, exit 10); pre-existing `userdata/` is preserved across extraction. `--dry-run` prints the plan (URL, expected size, free space, would-overwrite) without sending a single GET.

`snapshot clone` forks a chain DB into an isolated fixture in seconds: same filesystem → copy-on-write (APFS `clonefile` / Linux `FICLONE`, near-zero extra disk); across filesystems → a full byte copy with a warning (the JSON `method` is `clonefile` / `ficlone` / `copy`). `<dst>` must not exist; the source must be quiescent. `--from-node` resolves a stopped node's chain-DB dir from state — jar and docker **bind-mount** nodes on a **local** target (a default named-volume docker node is refused, since its DB isn't a host path).

### Monitoring

Deploy a Prometheus + Grafana stack alongside your node for visual dashboards. java-tron natively exposes Prometheus metrics on port 9527; trond embeds 5 pre-built Grafana dashboards from tron-docker and wires everything together.

```bash
# Single node with monitoring
trond apply --intent node.yaml --monitor

# Multi-node private network with monitoring
trond network create --intent net.yaml --monitor

# Health check includes monitoring containers
trond status my-node
trond network status
```

| Command | Description |
|---|---|
| `trond apply --monitor` | Deploy Prometheus + Grafana alongside the node |
| `trond network create --monitor` | Deploy a single monitoring stack for the entire network |
| `trond network add` | Automatically reloads Prometheus config to include the new node |
| `trond status <node>` / `trond network status` | Shows monitoring container health |
| `trond status` / `trond inspect <node> -o json` | Expose the stack's `prometheus_port` / `grafana_port` so agents can discover it |
| `trond remove <node>` / `trond network destroy` | Automatically cleans up the monitoring stack |

After deployment, Grafana is available at http://localhost:3000 (admin/admin) with 5 dashboards: java-tron-server, java-tron-api, java-tron-api-statistic, java-tron-mechanism, and node-exporter-full. Prometheus is at http://localhost:9090.

**Limitations**: The single Prometheus instance loses visibility into nodes isolated by `trond partition`; metrics resume after `trond heal`. Monitoring is Docker-only (Prometheus and Grafana run as containers), so jar-runtime targets need Docker on the trond machine for the monitoring stack.

### Test-harness SDK

These commands exist so external test tools can drive trond programmatically.

| Command | Description |
|---|---|
| `trond inspect [node \| --network \| --all]` | JSON manifest of endpoints, container IPs, runtime info |
| `trond exec <node> -- <cmd>` | Run a command inside a node (docker exec / host exec) |
| `trond files put <node> <local> <remote>` | Push a file into a node |
| `trond files get <node> <remote> <local>` | Pull a file from a node |
| `trond wait <node> --port \| --http \| --exec` | Block until a probe succeeds |
| `trond disconnect <a> <b>` / `connect` | Tear down / restore peer link at the docker network layer |
| `trond partition --groups 'a,b\|c,d'` / `heal` | Apply / reverse a network partition |
| `trond events [--follow] [--since <dur>]` | Stream the audit log as JSONL |
| `trond knowledge [topic]` | Query embedded deployment guidance |

### Global flags

| Flag / env | Effect |
|---|---|
| `--state-dir <dir>` / `TROND_STATE_DIR` | Relocate `state.json`, `audit.log`, `deployments/` (enables parallel enclaves) |
| `-o, --output text\|json` | Output format (json for machine consumers) |
| `--log-format text\|json` | Log format on stderr |
| `-q, --quiet` / `-v, --verbose` | Output verbosity |
| `--no-color` | Disable ANSI colors |
| `TROND_TEMPLATES_DIR` | Override the embedded HOCON templates |
| `TROND_SSH_ACCEPT_NEW_HOSTS=1` | TOFU mode for SSH host keys (refuses on mismatch — never trusts a key change) |

## Architecture

```
intent.yaml --> [validate] --> [render HOCON + compose/systemd]
                                       |
                               [plan / apply]
                                       |
                        [Docker Compose | Jar + systemd]
                                       |
                              [target: local | SSH]
                                       |
                               [state.json tracking]
```

**Trust ladder** -- progressive levels of automation:

1. `trond config validate` -- just check syntax
2. `trond plan` -- see what would change
3. `trond apply` -- deploy with confirmation gate
4. `trond apply --auto-approve` -- fully automated (CI mode)

## Test-harness Integration

trond is also designed to be **driven by an external test tool** (similar to
how Hive or Kurtosis drive Ethereum clients). The test tool brings the
assertions and scenario logic; trond provides the deployment substrate.

A typical CI scenario:

```bash
JOB=$CI_JOB_ID
T="trond --state-dir /tmp/trond-$JOB"

# 1. Bring up an isolated enclave with auto-allocated ports.
$T apply --intent test/private-3sr.yaml --auto-approve --wait --wait-timeout 5m

# 2. Subscribe to the event stream for the harness's own reporting.
$T events --follow > /tmp/trond-$JOB/events.jsonl &
EVENTS_PID=$!

# 3. Discover the topology.
$T inspect --all -o json > /tmp/trond-$JOB/topology.json

# 4. Inject a fixture and run a scripted scenario.
$T files put sr0 ./fixtures/genesis.conf /etc/tron/config.conf
$T exec fullnode0 -- curl -s http://127.0.0.1:8090/wallet/createaccount

# 5. Simulate a 2/2 SR partition and wait for the network to detect.
$T partition --groups 'sr0,sr1 | sr2,fullnode0'
$T wait fullnode0 --http '{http}/wallet/getnowblock' \
   --json-path block_header.raw_data.number --json-gt 100 --timeout 2m

# 6. Heal and tear down.
$T heal --groups 'sr0,sr1 | sr2,fullnode0'
$T network destroy --confirm test
kill $EVENTS_PID
```

The `--state-dir` plus `auto_ports: true` (in the intent's `target` block)
let multiple CI jobs run in parallel against the same Docker daemon without
colliding. Every command's `-o json` output is structured for downstream
consumption.

## Intent Reference

The full set of fields trond accepts in an intent file. See
`schemas/intent.schema.json` for the machine-readable JSON Schema.

### Top-level

```yaml
name: <dns-label>             # required, ^[a-z0-9][a-z0-9-]{0,62}$
network: mainnet|nile|private # required
target: { ... }               # required, see below
nodes: [ { ... }, ... ]       # required, at least one node
```

### Target

```yaml
target:
  type: local|ssh             # required
  runtime: docker|jar         # default: docker
  host: <ip-or-hostname>      # required for ssh
  user: <username>            # required for ssh
  port: 22                    # ssh port
  identity_file: ~/.ssh/...   # ssh key, supports ~ expansion
  auto_ports: false           # true → trond allocates free OS ports
                              # (essential for parallel test enclaves)
```

### Node

```yaml
nodes:
  - type: fullnode|witness|solidity|lite     # required
    version: latest                          # image tag for docker / jar version
    image: tronprotocol/java-tron            # docker image
    install_path: /opt/tron                  # jar runtime install root
    process_manager: systemd|nohup           # jar runtime, default systemd
    system_user: tron                        # jar runtime user

    # Witness-only
    witness_key:                             # preferred: structured block
      private_key_env: SR_PRIVATE_KEY        # env var holding the hex key (inlined into HOCON at apply)
      keystore_path: /opt/tron/ks.json       # OR path to keystore inside container
      keystore_password_env: KS_PASS         # password env (forwarded to container)
      account_address: TXxxx...              # localWitnessAccountAddress (delegated)
    witness_key_env: SR_PRIVATE_KEY          # legacy shorthand (still works)

    # Resources
    resources:
      memory: 16GB                           # used for memory limit + JVM heap default
      cpu: "2.0"                             # docker compose cpus

    # JVM tuning (all opt-in; defaults won't fight the upstream image)
    jvm:
      heap_max: 14g                          # explicit override
      heap_new: 4g
      direct_memory: 1g
      gc: G1|CMS|auto                        # default auto = no GC flags
      gc_log: false                          # opt-in to verbose GC logging

    # Container ports (also written to HOCON)
    ports:
      http: 8090
      grpc: 50051
      solidity_http: 8091
      solidity_grpc: 50061
      jsonrpc: 8545
      p2p: 18888
      metrics: 9527

    # Storage — chain DB + logs persistence
    # Each accepts an absolute path (bind-mount) or bare name (named volume).
    # If `path` is set, data → <path>/data, logs → <path>/logs.
    storage:
      data: /var/lib/tron                    # OR named-volume: shared-tron-db
      logs: /var/log/tron
      path: /opt/tron/main                   # single-root convenience

    # Feature toggles (tri-state *bool)
    features:
      metrics: true                          # exposes Prometheus on ports.metrics
      jsonrpc: true                          # enables EVM JSON-RPC
      rate_limit: true                       # default true
      event_subscribe: false

    # Runtime / lifecycle
    restart: unless-stopped                  # docker policy; jar maps to systemd
    extra_env:                               # arbitrary env vars (witness key auto-inlined into HOCON, not env)
      LOG_LEVEL: DEBUG
    extra_args:                              # extra CLI flags after -c <conf>
      - --debug
    labels:                                  # docker labels
      role: api

    # HOCON network overrides (typed first-class fields)
    network_overrides:
      seeds: ["10.0.0.1:18888"]              # → seed.node.ip.list
      active_peers: ["10.0.0.1:18888"]       # → node.active (auto-wired by network create)
      passive_peers: []                      # → node.passive
      p2p_version: 99999                     # → node.p2p.version (use unique value to isolate test enclaves)
      discovery: false                       # → node.discovery.enable
      max_connections: 8                     # → node.maxConnections
      max_active_same_ip: 8                  # → node.maxActiveNodesWithSameIp
      need_sync_check: false                 # → block.needSyncCheck (must be false for first SR of new private net)

    # HOCON long-tail escape hatch — any dotted key
    config_overrides:
      "vm.supportConstant": true
      "block.maintenanceTimeInterval": 30000
      "storage.db.engine": "ROCKSDB"

    # Compose-only fields (no-op for jar)
    networks: [tron-mesh]                    # join external docker networks
    depends_on: [seed-node]                  # service ordering
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:8090/wallet/getnowblock"]
      interval: 10s
      timeout: 3s
      retries: 5
      start_period: 30s
    ulimits:
      nofile: 65535
    extra_hosts:
      backup-node: 10.0.0.99
    entrypoint: ["/usr/local/bin/wrapper"]   # only if you really need to override
    logging:
      driver: json-file
      options:
        max-size: 100m
        max-file: "10"
    shm_size: 256m
```

### How HOCON overrides land

The render layers per-node HOCON in two passes (last-write-wins per HOCON spec):

1. **In-place rewrites** keep the upstream template legible: `ports.*` and
   `features.jsonrpc` get patched at the existing line.
2. **Trailing override block** (`# === trond overrides ===`) is appended
   to the end of the conf carrying every value derived from
   `network_overrides`, `witness_key`, and `config_overrides`. HOCON's
   merge semantics make these win regardless of where the template wrote
   the same key.

The witness private key is **inlined** into this block at apply time —
java-tron's typesafe-config does not perform `${ENV}` substitution.
The conf file's 0600 perms keep the secret on disk, never echoed to stdout.

### Container filesystem layout

trond aligns with the official `tronprotocol/java-tron` image (built from
`tron-docker tools/docker/Dockerfile`):

| Path | Mounted from |
|---|---|
| `/java-tron/conf/<name>.conf` | rendered HOCON (read-only) |
| `/java-tron/output-directory` | chain DB volume / bind |
| `/java-tron/logs` | log volume / bind |

The image's entrypoint (`./bin/docker-entrypoint.sh`) execs
`./bin/FullNode` with the args trond emits — no `java -jar` workarounds.

## Repository Evolution — From Templates to a CLI

This repository started life as a curated set of HOCON config files for
java-tron. Operators would `wget` or `git clone` the file matching their
network and then hand-edit it. That workflow is still supported (see
[Configuration Templates](#configuration-templates) — the same files
sit at the repo root and get refreshed from upstream on every release).

What this repo *also* provides now is a small, opinionated CLI that
removes the hand-editing step. The same templates are embedded in the
`trond` binary; an `intent.yaml` describes the deviations from the
default template; `trond config render` produces the final HOCON
deterministically; `trond apply` deploys it.

| Workflow | Before | Now (optional) |
|---|---|---|
| Get a template | `git clone` + open `main_net_config.conf` | `trond config render <intent.yaml>` |
| Tweak ports / features | Edit the `.conf` directly | Set `ports:` / `features:` in intent |
| Apply changes to a node | scp + restart by hand | `trond apply --intent <file>` (idempotent) |
| Multi-node private network | Repeat the above N times | `trond network create --intent <file>` |
| Look up "what does this HOCON key mean?" | grep / docs / java-tron source | `trond config docs <key>` |

Both flows coexist:

- **Pure template users** can still `cat main_net_config.conf` or `git pull`
  this repo for the latest mainnet config, ignore `bin/`, and never touch
  the CLI.
- **CLI users** never need to edit the `.conf` files directly — `trond`
  handles rendering, ports, validation, and lifecycle.

The CLI lives under `cmd/` and `internal/`. The original config files
remain at the repo root. `make sync-templates` re-fetches them from
upstream into both the root and the CLI's embedded copy so the two stay
in lockstep.

## Companion Tools

In addition to the `trond` CLI, this repo ships sibling Go tools under
`tools/` for adjacent test/operations workflows. They build into separate
binaries and have separate release tarballs.

| Tool | Path | Purpose                                                                                                                                                                                                        |
|---|---|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **`replay`** | [`tools/replay/`](tools/replay/README.md) | Streams mainnet historical transactions from TronGrid HTTP API to a private chain. Pure-Go external HTTP client (no java-tron source dependency). Used for testing setup, stress harness, snapshot validation. |

Build:

```bash
make build-replay        # → bin/replay
make install-replay      # → $(GOBIN)/replay
```

Each tool ships as its own tarball (`replay_<version>_<os>_<arch>.tar.gz`)
in releases, alongside the main `trond` tarball.

## Configuration Templates

Base java-tron configuration templates rendered into per-node HOCON. The
mainnet and Nile templates track upstream — periodically refresh from the
authoritative sources before tagging a release:

| File | Network | Upstream source of truth |
|---|---|---|
| `main_net_config.conf` | Mainnet | https://github.com/tronprotocol/java-tron/blob/develop/framework/src/main/resources/config.conf |
| `test_net_config.conf` | Nile testnet | https://github.com/tron-nile-testnet/nile-testnet/blob/master/framework/src/main/resources/config-nile.conf |
| `private_net_config.conf` | Private network | maintained in-repo (no upstream) |

Refresh from upstream:

```bash
make sync-templates    # fetches mainnet + nile, leaves private alone
```

After a sync, run `make test` and `./bin/trond config validate examples/*.yaml`
to confirm nothing broke. Keep both copies in sync — `templates/<file>` is a
symlink pointing at the root `<file>`, and `internal/render/templates/<file>`
is the embedded copy used at runtime.

## Examples

See `examples/` for sample intent files:

- `mainnet-fullnode.yaml` -- standard mainnet fullnode
- `mainnet-fullnode-snapshot.yaml` -- mainnet fullnode that picks up a pre-extracted chain DB snapshot
- `nile-fullnode.yaml` -- Nile testnet fullnode
- `mainnet-witness.yaml` -- mainnet witness (Super Representative)
- `private-network.yaml` -- multi-node private network
- `remote-ssh-fullnode.yaml` -- deploy to remote host via SSH

## For AI agents calling trond

Read [`AGENTS.md`](AGENTS.md) at the repo root. It documents the JSON
contract, exit-code semantics, retry guidance, four core workflows
(deploy / diagnose / snapshot / private network), and concurrency
isolation via `TROND_STATE_DIR`. Designed so an agent that reads it
once can drive trond without trial-and-error against `--help` output.

## Development

```bash
make build      # Build trond binary
make test       # Run unit tests
make lint       # Run golangci-lint
make e2e        # End-to-end tests (requires Docker)
make build-all  # Cross-compile all platforms
```

## License

See [LICENSE](LICENSE).

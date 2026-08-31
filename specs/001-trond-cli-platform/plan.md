# Implementation Plan: trond CLI Deployment Platform

**Branch**: `001-trond-cli-platform` | **Date**: 2026-04-08 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/001-trond-cli-platform/spec.md`

## Summary

Redesign the tron-deployment repository from a passive HOCON configuration template
repository into an AI-friendly, CLI-first java-tron node deployment platform. The sole
deliverable is a Go binary called `trond` that renders declarative intent files into
java-tron configurations, deploys nodes via Docker or native jar+systemd, manages their
lifecycle, and outputs structured JSON for CI pipelines and AI agents.

The implementation extends the existing `tron-docker/tools/trond/` Cobra CLI codebase,
adding intent-based rendering, multi-runtime (docker/jar) and multi-target (local/ssh)
abstractions, plan/apply semantics, and structured diagnosis.

## Technical Context

**Language/Version**: Go 1.25+ (validator/v10 dependency requires it)
**Primary Dependencies**:
- `github.com/spf13/cobra` v1.9+ — CLI framework (already in tron-docker trond)
- `github.com/spf13/viper` — config management (already imported)
- `gopkg.in/yaml.v3` — intent YAML parsing
- `github.com/go-playground/validator/v10` — intent schema validation
- `golang.org/x/crypto/ssh` — SSH target operations
- `github.com/santhosh-tekuri/jsonschema/v6` — JSON Schema validation for intent
- HOCON: `github.com/gurkankaymak/hocon` — Go HOCON parser for config rendering

**Storage**: Local state file `~/.trond/state.json` (V1); audit log `~/.trond/audit.log`
**Testing**: `go test` + testify for unit; containerized e2e in CI
**Target Platform**: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
**Project Type**: CLI tool (single static binary)
**Performance Goals**: All commands complete within 5s excluding network I/O (downloads, SSH)
**Constraints**: Binary size < 30MB; zero runtime dependencies; no CGO
**Scale/Scope**: Manages 1–20 nodes per operator in V1

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Evidence |
|-----------|--------|----------|
| I. CLI-First, AI-Friendly | ✅ Pass | Single `trond` binary; all commands support `--output json`; CLAUDE.md planned |
| II. Declarative Intent | ✅ Pass | intent.yaml → renderer → HOCON + compose/systemd; intent is git-trackable |
| III. Reuse Over Reinvention | ✅ Pass | Extends tron-docker trond (Cobra CLI); consumes tron-docker images/compose; ports start.sh JVM tuning |
| IV. Multi-Runtime + Multi-Target | ✅ Pass | docker + jar runtimes; local + ssh targets; both in V1 |
| V. Pipeline First-Class | ✅ Pass | plan/apply separation; semantic exit codes; `setup-trond` action planned |
| VI. Static Binary Distribution | ✅ Pass | Go cross-compilation; sigstore signing; semver |
| VII. Verifiable Security | ✅ Pass | PrivateKey type with REDACTED String(); env-only injection; SSH whitelist; audit log |
| VIII. Progressive Trust | ✅ Pass | alpha → beta → 1.0 release ladder; no premature "production-ready" claims |

No violations. Gate passes.

## Project Structure

### Documentation (this feature)

```text
specs/001-trond-cli-platform/
├── plan.md              # This file
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (CLI contract)
└── tasks.md             # Phase 2 output (/speckit-tasks)
```

### Source Code (repository root)

```text
tron-deployment/
├── CLAUDE.md                         # AI agent entry guide
├── README.md                         # Human documentation (rewritten)
├── SECURITY.md                       # Vulnerability disclosure policy
├── go.mod                            # Go module definition
├── go.sum
├── Makefile                          # Build, test, lint, release targets
├── .goreleaser.yaml                  # Cross-compilation + signing
│
├── main.go                           # Entrypoint → cmd/root.go
│
├── cmd/                              # Cobra command tree
│   ├── root.go                       # Root command, global flags (--output, --log-format)
│   ├── apply.go                      # trond apply (canonical deploy)
│   ├── plan.go                       # trond plan (dry-run diff)
│   ├── start.go                      # trond start <node>
│   ├── stop.go                       # trond stop <node>
│   ├── restart.go                    # trond restart <node>
│   ├── remove.go                     # trond remove <node>
│   ├── list.go                       # trond list
│   ├── status.go                     # trond status [<node>]
│   ├── logs.go                       # trond logs <node>
│   ├── health.go                     # trond health <node>
│   ├── diagnose.go                   # trond diagnose <node>
│   ├── verify.go                     # trond verify <intent>
│   ├── upgrade.go                    # trond upgrade <node> --version X
│   ├── rollback.go                   # trond rollback <node>
│   ├── preflight.go                  # trond preflight <target>
│   ├── bootstrap.go                  # trond bootstrap <target>
│   ├── knowledge.go                  # trond knowledge <topic>
│   ├── config/                       # trond config subcommands
│   │   ├── validate.go
│   │   ├── render.go
│   │   ├── diff.go
│   │   └── explain.go
│   └── network/                      # trond network subcommands
│       ├── create.go
│       ├── status.go
│       └── destroy.go
│
├── internal/                         # Internal packages (not importable)
│   ├── intent/                       # Intent parsing & validation
│   │   ├── schema.go                 # Intent YAML struct definitions
│   │   ├── loader.go                 # Load + validate intent files
│   │   └── overlay.go                # Base/overlay merge logic
│   │
│   ├── render/                       # Configuration rendering
│   │   ├── hocon.go                  # HOCON template loading + patching
│   │   ├── compose.go                # docker-compose.yaml generation
│   │   ├── systemd.go                # systemd unit file generation
│   │   └── jvm.go                    # JVM parameter tuning (ported from start.sh)
│   │
│   ├── runtime/                      # Runtime abstraction
│   │   ├── runtime.go                # Runtime interface
│   │   ├── docker.go                 # Docker runtime (docker compose exec)
│   │   └── jar.go                    # Jar runtime (systemd, jar download)
│   │
│   ├── target/                       # Target abstraction
│   │   ├── target.go                 # Target interface
│   │   ├── local.go                  # Local target (exec on host)
│   │   └── ssh.go                    # SSH target (remote execution)
│   │
│   ├── state/                        # State management
│   │   ├── store.go                  # State file read/write
│   │   ├── lock.go                   # Deployment locking
│   │   └── types.go                  # NodeState, DeploymentRecord types
│   │
│   ├── diagnosis/                    # Health checks & diagnosis
│   │   ├── checker.go                # Check runner interface
│   │   ├── sync.go                   # Block sync check
│   │   ├── peers.go                  # Peer count check
│   │   ├── disk.go                   # Disk space check
│   │   ├── ports.go                  # Port availability check
│   │   └── version.go               # Version mismatch check
│   │
│   ├── security/                     # Security primitives
│   │   ├── privatekey.go             # PrivateKey type (REDACTED String/JSON)
│   │   ├── privatekey_test.go        # MUST verify key never leaks
│   │   ├── ssh_whitelist.go          # Allowed remote commands
│   │   └── audit.go                  # Audit log writer
│   │
│   ├── output/                       # Structured output
│   │   ├── json.go                   # JSON formatter
│   │   ├── text.go                   # Human-readable formatter
│   │   ├── error.go                  # Structured error (code, message, suggestions)
│   │   └── exitcode.go              # Exit code constants + table
│   │
│   └── knowledge/                    # Bundled knowledge files
│       └── embed.go                  # go:embed for knowledge/*.md
│
├── knowledge/                        # Knowledge markdown files (embedded at build)
│   ├── node-types.md
│   ├── troubleshooting.md
│   ├── best-practices.md
│   ├── cloud-deployment.md
│   └── config-reference.md
│
├── templates/                        # Configuration base templates
│   ├── main_net_config.conf          # Symlink or copy from root
│   ├── test_net_config.conf
│   └── private_net_config.conf
│
├── examples/                         # Example intent files
│   ├── mainnet-fullnode.yaml
│   ├── nile-fullnode.yaml
│   ├── mainnet-witness.yaml
│   ├── private-network.yaml
│   └── remote-ssh-fullnode.yaml
│
├── schemas/                          # Published schemas
│   └── intent.schema.json            # JSON Schema for intent.yaml
│
├── infra/                            # Optional IaC templates
│   └── terraform/
│       └── aws/
│
├── main_net_config.conf              # Preserved base config (existing)
├── test_net_config.conf              # Preserved base config (existing)
└── private_net_config.conf           # Preserved base config (existing)
```

**Structure Decision**: Single Go module at repository root. Internal packages under
`internal/` enforce encapsulation. The `cmd/` tree maps 1:1 to CLI subcommands via Cobra.
Existing `.conf` files remain at root for backward compatibility; `templates/` symlinks to
them for the renderer.

## Complexity Tracking

No Constitution violations. Table not needed.

---

## Phase 0: Research

### Research 1: Go HOCON Parsing

**Decision**: Use `github.com/gurkankaymak/hocon` for HOCON parsing.
**Rationale**: It is the most actively maintained Go HOCON library (supports includes,
substitutions, multi-line strings). java-tron configs use all these features.
**Alternatives considered**:
- Manual regex-based patching: fragile, breaks on nested structures.
- Calling java-tron's own parser via subprocess: adds JDK dependency, defeats static binary goal.
- Converting .conf to JSON first: lossy (HOCON comments, includes lost).

**Risk**: HOCON edge cases (substitutions, includes) may not be 100% compatible.
**Mitigation**: Add a `config render` validation step that loads the output with the
same parser and compares key paths. Maintain a test suite of known java-tron configs.

### Research 2: SSH Execution Strategy

**Decision**: Use `golang.org/x/crypto/ssh` for direct SSH sessions. No dependency on
local `ssh` binary.
**Rationale**: Eliminates host SSH config inconsistencies, enables proper error handling,
and supports programmatic key loading from intent's `identity_file` path.
**Alternatives considered**:
- `os/exec` to shell `ssh` binary: fragile, depends on host config, harder to capture structured errors.
- Docker context over SSH (`DOCKER_HOST=ssh://`): only helps docker runtime, not jar.

**File transfer**: Use SFTP subsystem via the same SSH connection for uploading configs,
jar files, and systemd units.

### Research 3: JVM Tuning Port from start.sh

**Decision**: Port the JVM parameter calculation algorithm from `java-tron/start.sh`
(lines ~180-280) into `internal/render/jvm.go` as a pure Go function.
**Rationale**: The algorithm reads total system memory and calculates `-Xmx`, `-Xmn`,
`-XX:MaxDirectMemorySize`, and GC options. Porting it preserves production-proven defaults
while enabling unit testing and intent-level overrides.
**Key logic to port**:
- Memory-based heap sizing (14g for 32GB+ systems, scaled down for smaller)
- G1 vs CMS GC selection (G1 for JDK 17+, CMS for JDK 8)
- GC log path configuration
- Direct memory sizing (typically Xmx/14)

### Research 4: State File Design

**Decision**: `~/.trond/state.json` — a single JSON file containing an array of managed
nodes, each recording: name, intent hash, config hash, deployed version, target, runtime,
status, last-applied timestamp.
**Rationale**: Simple, human-readable, git-diffable. No database dependency.
Lock via OS-level `flock()` on `~/.trond/state.lock` to prevent concurrent writes.
**Alternatives considered**:
- SQLite: overkill for <20 nodes; adds CGO dependency.
- Separate file per node: harder to list atomically.
- Remote backends (S3/etcd): deferred to V2 per spec.

### Research 5: Docker Compose Execution

**Decision**: Continue tron-docker trond's approach: `os/exec` to `docker compose` CLI
(Compose V2 plugin).
**Rationale**: No Docker SDK dependency; compose files are the canonical deployment
artifact users can inspect/edit; matches existing tron-docker patterns.
**Version check**: `trond preflight` MUST verify `docker compose version` returns v2.x.

### Research 6: Cosign / SLSA Signing at Release

**Decision**: Use GoReleaser + cosign + slsa-github-generator.
**Rationale**: GoReleaser handles cross-compilation, checksums, and GitHub Releases.
The `cosign-installer` + `slsa-github-generator` GitHub Actions add keyless signing
and SLSA L3 provenance with ~20 lines of workflow YAML.

---

## Phase 1: Design

### Data Model

(Full details in [data-model.md](data-model.md))

**Core types**:

```
Intent {
  name: string (required, unique per state)
  target: Target
  network: enum(mainnet, nile, private)
  nodes: []NodeSpec
}

Target {
  type: enum(local, ssh)
  host?: string
  user?: string
  identity_file?: string
  runtime: enum(docker, jar) = docker
}

NodeSpec {
  type: enum(fullnode, witness, solidity, lite)
  version?: string (default: latest)
  features: Features
  resources: Resources
  jvm?: JVMConfig
  witness_key_env?: string
}

ManagedNode {
  name: string
  intent_hash: string
  config_hash: string
  version: string
  target: Target
  runtime: enum(docker, jar)
  status: enum(running, stopped, error, unknown)
  last_applied: timestamp
  previous_version?: string (for rollback)
}
```

### Interface Contracts

(Full details in [contracts/](contracts/))

**CLI contract**: Every command follows:
- Input: flags + optional intent file
- Output: JSON to stdout (when `--output json`), errors to stderr
- Exit codes: 0=success, 1=general-error, 2=validation-error, 3=target-unreachable,
  4=preflight-failure, 5=partial-success, 10=human-intervention-required

**Error contract**:
```json
{
  "code": "PORT_CONFLICT",
  "message": "Port 8090 already in use by container 'old-tron'",
  "suggestions": [
    "Stop the conflicting container: docker stop old-tron",
    "Or change the HTTP port in your intent.yaml"
  ]
}
```

**Status contract**:
```json
{
  "name": "my-fullnode",
  "status": "running",
  "network": "mainnet",
  "runtime": "docker",
  "version": "4.8.1",
  "block_height": 58234567,
  "sync_progress_percent": 99.8,
  "peer_count": 45,
  "is_synced": true,
  "uptime": "3d 12h 5m",
  "api_endpoints": {
    "http": "http://localhost:8090",
    "grpc": "localhost:50051",
    "jsonrpc": "http://localhost:8545"
  }
}
```

### Runtime Interface

```go
type Runtime interface {
    Deploy(ctx context.Context, node NodeSpec, rendered RenderedConfig, target Target) error
    Start(ctx context.Context, name string, target Target) error
    Stop(ctx context.Context, name string, target Target) error
    Remove(ctx context.Context, name string, target Target, purge bool) error
    Status(ctx context.Context, name string, target Target) (*NodeStatus, error)
    Logs(ctx context.Context, name string, target Target, opts LogOpts) (io.ReadCloser, error)
}
```

Two implementations: `DockerRuntime` (calls `docker compose`) and `JarRuntime` (calls
`systemctl` + manages jar files).

### Target Interface

```go
type Target interface {
    Exec(ctx context.Context, cmd string, args ...string) ([]byte, error)
    Upload(ctx context.Context, localPath, remotePath string) error
    Download(ctx context.Context, remotePath, localPath string) error
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error
    DiskFree(ctx context.Context, path string) (uint64, error)
    MemTotal(ctx context.Context) (uint64, error)
}
```

Two implementations: `LocalTarget` (os/exec) and `SSHTarget` (x/crypto/ssh + SFTP).

### Apply Pipeline (core flow)

```
trond apply --intent <file>
  │
  ├─ 1. Load intent.yaml + validate schema
  ├─ 2. Resolve target (local or SSH connect test)
  ├─ 3. Acquire state lock
  ├─ 4. Load current state for this node name
  ├─ 5. Render: intent + base template → final HOCON config
  ├─ 6. Render: intent → docker-compose.yaml OR systemd unit
  ├─ 7. Compute diff vs deployed state
  │     ├─ No changes → exit 0, "no changes"
  │     └─ Changes detected → continue
  ├─ 8. For each apply step (idempotent check-then-act; apply only):
  │     ├─ [jar] Download jar if missing/wrong hash
  │     ├─ Upload config files to target
  │     ├─ [docker] Upload compose file + docker compose up -d
  │     ├─ [jar] Upload systemd unit + systemctl daemon-reload + start
  │     └─ Each step: check artifact exists & correct → skip if so
  ├─ 9. Update state file (name, hashes, version, timestamp)
  ├─ 10. Release lock
  └─ 11. Output JSON result
```

### Constitution Re-Check (Post-Design)

| Principle | Status |
|-----------|--------|
| I. CLI-First | ✅ Every command in cmd/ → Cobra; `--output json` via internal/output |
| II. Declarative Intent | ✅ intent.yaml → internal/intent → internal/render pipeline |
| III. Reuse | ✅ Extends tron-docker Cobra base; docker compose exec; base .conf templates |
| IV. Multi-Runtime/Target | ✅ Runtime + Target interfaces with docker/jar + local/ssh implementations |
| V. Pipeline | ✅ plan/apply split; semantic exit codes in exitcode.go; verify command |
| VI. Static Binary | ✅ Go + GoReleaser; no CGO; cross-compile 4 platforms |
| VII. Security | ✅ PrivateKey type; ssh_whitelist.go; audit.go; cosign signing |
| VIII. Progressive Trust | ✅ alpha tagging; README trust ladder; SECURITY.md |

All gates pass post-design.

---

## Phase 1 Artifacts

Generating the remaining Phase 1 documents now.

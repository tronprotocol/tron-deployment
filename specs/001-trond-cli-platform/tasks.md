# Tasks: trond CLI Deployment Platform

**Input**: Design documents from `/specs/001-trond-cli-platform/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/cli-contract.md

**Tests**: Included only for security-critical and core pipeline logic. Not TDD across the board.

**Organization**: Tasks grouped by user story for independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story (US1–US8)
- All paths relative to repository root

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Go module initialization, project skeleton, build system

- [x] T001 Initialize Go module with `go mod init github.com/tronprotocol/tron-deployment` in go.mod
- [x] T002 Create Makefile with targets: build, test, lint, e2e, build-all, clean
- [x] T003 [P] Create .goreleaser.yaml for cross-compilation (linux/darwin × amd64/arm64)
- [x] T004 [P] Create .golangci.yml with standard Go linting rules
- [x] T005 Create main.go entrypoint that calls cmd/root.go Execute()
- [x] T006 Create cmd/root.go with Cobra root command, global flags (--output, --log-format, --quiet, --verbose, --no-color)
- [x] T007 [P] Create schemas/intent.schema.json defining the intent YAML JSON Schema
- [x] T008 [P] Move existing .conf files: symlink templates/main_net_config.conf → main_net_config.conf (same for test_net, private_net)
- [x] T009 [P] Create example intent files: examples/mainnet-fullnode.yaml, examples/nile-fullnode.yaml, examples/mainnet-witness.yaml, examples/private-network.yaml, examples/remote-ssh-fullnode.yaml

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core internal packages that ALL user stories depend on

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [x] T010 [P] Implement internal/output/exitcode.go with exit code constants (0,1,2,3,4,5,10) per contracts/cli-contract.md
- [x] T011 [P] Implement internal/output/error.go with structured error type (code, message, suggestions) and JSON/text formatters
- [x] T012 [P] Implement internal/output/json.go and internal/output/text.go output formatters with --output flag dispatch
- [x] T013 [P] Implement internal/security/privatekey.go with PrivateKey type: String() returns "[REDACTED]", MarshalJSON returns "[REDACTED]"
- [x] T014 [P] Implement internal/security/privatekey_test.go verifying key NEVER leaks in String, JSON, error formatting, or fmt.Sprintf
- [x] T015 Implement internal/intent/schema.go with Go struct definitions for Intent, Target, NodeSpec, Features, Resources, JVMConfig, PortMapping per data-model.md
- [x] T016 Implement internal/intent/loader.go: load YAML, validate against schema, return typed Intent. Reject plaintext keys in witness_key_env.
- [x] T017 Implement internal/intent/overlay.go: merge base intent with overlay intent (for multi-environment support)
- [x] T018 Implement internal/render/hocon.go: load base .conf template, apply intent-driven overrides (network, ports, features, p2p version), write final HOCON
- [x] T019 Implement internal/render/jvm.go: port JVM tuning logic from java-tron start.sh — heap sizing by system memory, G1/CMS selection, GC log config
- [x] T020 [P] Implement internal/render/compose.go: generate docker-compose.yaml from intent (image, ports, volumes, memory, JVM args, config path)
- [x] T021 [P] Implement internal/render/systemd.go: generate systemd unit file from intent (ExecStart, Environment, MemoryMax, User, WorkingDirectory)
- [x] T022 Implement internal/target/target.go: Target interface (Exec, Upload, Download, ReadFile, WriteFile, DiskFree, MemTotal)
- [x] T023 [P] Implement internal/target/local.go: LocalTarget using os/exec and local filesystem
- [x] T024 [P] Implement internal/target/ssh.go: SSHTarget using golang.org/x/crypto/ssh + SFTP for file transfer
- [x] T025 Implement internal/runtime/runtime.go: Runtime interface (Deploy, Start, Stop, Remove, Status, Logs)
- [x] T026 [P] Implement internal/runtime/docker.go: DockerRuntime — docker compose up/down/ps/logs via target.Exec
- [x] T027 [P] Implement internal/runtime/jar.go: JarRuntime — jar download with SHA256 verify, systemctl start/stop/status/journal via target.Exec
- [x] T028 Implement internal/state/types.go: ManagedNode struct, DeploymentRecord, state file schema per data-model.md
- [x] T029 Implement internal/state/store.go: load/save ~/.trond/state.json, CRUD operations on node entries, intent/config hash comparison
- [x] T030 Implement internal/state/lock.go: flock-based locking on ~/.trond/state.lock
- [x] T031 [P] Implement internal/security/audit.go: append-only JSONL audit log writer to ~/.trond/audit.log
- [x] T032 [P] Implement internal/security/ssh_whitelist.go: allowed remote command whitelist, reject arbitrary shell from intent fields

**Checkpoint**: Foundation ready — all internal packages available for command implementations

---

## Phase 3: User Story 1 — Deploy Mainnet Fullnode Locally via Docker (P1) 🎯 MVP

**Goal**: `trond apply --intent examples/mainnet-fullnode.yaml` deploys a running Docker fullnode locally with JSON output

**Independent Test**: Run apply on local machine with Docker → container running → `trond status` returns block height → `trond stop` stops it

### Implementation for User Story 1

- [x] T033 [US1] Implement cmd/config/validate.go: `trond config validate <intent>` — load + validate intent, exit 0 or 2
- [x] T034 [US1] Implement cmd/config/render.go: `trond config render <intent>` — render HOCON + compose/systemd to stdout or --output-dir
- [x] T035 [US1] Implement cmd/apply.go: `trond apply --intent <file>` — full pipeline: validate → resolve target → acquire lock → render → diff → deploy → update state → release lock → output JSON. Register `deploy` as alias.
- [x] T036 [US1] Implement cmd/list.go: `trond list` — read state file, output all managed nodes as JSON or table
- [x] T037 [US1] Implement cmd/stop.go: `trond stop <node>` — look up node in state, call runtime.Stop, update state
- [x] T038 [US1] Implement cmd/start.go: `trond start <node>` — look up node in state, call runtime.Start, update state
- [x] T039 [US1] Implement cmd/remove.go: `trond remove <node> [--keep-data] [--confirm <name>]` — stop + remove container/unit, optionally purge data, remove from state
- [x] T040 [US1] Implement cmd/logs.go: `trond logs <node> [--tail N] [--follow]` — delegate to runtime.Logs
- [x] T041 [US1] Write e2e test: deploy mainnet fullnode locally via docker, verify status, stop, remove

**Checkpoint**: US1 complete. `trond apply` → running Docker fullnode → `trond status` → `trond stop` works end-to-end.

---

## Phase 4: User Story 2 — Deploy Witness Node on Remote Server via Jar (P1)

**Goal**: `SR_PRIVATE_KEY=<key> trond apply --intent mainnet-witness.yaml` deploys a witness to an SSH target with systemd

**Independent Test**: Deploy to SSH-reachable server → systemd service running → private key only in env, not on disk

### Implementation for User Story 2

- [x] T042 [US2] Implement jar download + SHA256 verification logic in internal/runtime/jar.go (download from java-tron GitHub Releases)
- [x] T043 [US2] Implement systemd unit upload + daemon-reload + start in JarRuntime.Deploy
- [x] T044 [US2] Implement env var injection for witness_key_env in JarRuntime: read from os.Getenv, pass via systemd Environment= directive (NEVER write to file)
- [x] T045 [US2] Implement cmd/preflight.go: `trond preflight --intent <file>` — check target: SSH reachable, JDK version, Docker version, ports free, disk space, memory
- [x] T046 [US2] Implement cmd/bootstrap.go: `trond bootstrap --intent <file>` — install Docker or JDK on target (invoke packaged install scripts)
- [x] T047 [US2] Extend cmd/apply.go to handle ssh target + jar runtime path (target resolution, SFTP upload)
- [x] T048 [US2] Extend cmd/status.go to query remote nodes via SSH (systemctl status + curl HTTP API)
- [ ] T049 [US2] Write e2e test: deploy witness to a Docker-based SSH target (sshd container), verify systemd service running, verify private key not on disk

**Checkpoint**: US2 complete. SSH + jar + systemd + witness key injection works end-to-end.

---

## Phase 5: User Story 3 — Preview Deployment Changes (P1)

**Goal**: `trond plan --intent <file> --output json` shows structured diff without touching the target

**Independent Test**: Deploy a node, modify intent, run plan → JSON diff shows changes → no actual changes applied

### Implementation for User Story 3

- [x] T050 [US3] Implement cmd/plan.go: `trond plan --intent <file>` — validate → render → diff against state → output changes/destructive/downtime without applying
- [x] T051 [US3] Implement cmd/config/diff.go: `trond config diff <intent>` — diff rendered config vs deployed config
- [x] T052 [US3] Implement diff logic in internal/state/store.go: compare intent_hash + config_hash, enumerate field-level changes
- [x] T053 [US3] Extend cmd/apply.go to show plan and prompt for confirmation when changes detected (unless --auto-approve)

**Checkpoint**: US3 complete. Plan/apply separation works. CI can plan → review → apply.

---

## Phase 6: User Story 4 — Check Node Status and Diagnose Issues (P2)

**Goal**: `trond diagnose <node> --output json` returns structured health report with fix suggestions

**Independent Test**: Deploy a node → `trond diagnose` returns all-pass → stop node → diagnose returns warnings

### Implementation for User Story 4

- [x] T054 [US4] Implement cmd/status.go enhancements: full JSON output per contracts/cli-contract.md (block_height, sync_progress, peer_count, is_synced, uptime, endpoints)
- [x] T055 [US4] Implement cmd/health.go: `trond health <node>` — probe HTTP API + check sync progress
- [x] T056 [P] [US4] Implement internal/diagnosis/checker.go: Check interface (Name, Run → CheckResult{status, message, suggestions})
- [x] T057 [P] [US4] Implement internal/diagnosis/sync.go: block height comparison with network latest
- [x] T058 [P] [US4] Implement internal/diagnosis/peers.go: peer count threshold check
- [x] T059 [P] [US4] Implement internal/diagnosis/disk.go: disk space check on data directory
- [x] T060 [P] [US4] Implement internal/diagnosis/ports.go: port listening verification
- [x] T061 [P] [US4] Implement internal/diagnosis/version.go: deployed version vs latest release check
- [x] T062 [US4] Implement cmd/diagnose.go: `trond diagnose <node>` — run all checks, aggregate into overall/checks JSON

**Checkpoint**: US4 complete. Status, health, and diagnose all return structured actionable output.

---

## Phase 7: User Story 5 — Deploy via CI/CD Pipeline (P2)

**Goal**: GitHub Actions workflow can validate → plan → apply → verify with zero human intervention

**Independent Test**: Create a test workflow, run it, verify end-to-end pipeline completes

### Implementation for User Story 5

- [x] T063 [US5] Implement cmd/verify.go: `trond verify --intent <file> [--timeout 10m]` — poll node status until healthy or timeout, exit non-zero on failure
- [x] T064 [US5] Create .github/actions/setup-trond/action.yml: download + verify + cache trond binary, add to PATH
- [x] T065 [US5] Create .github/workflows/e2e-test.yml: build trond → deploy local fullnode → verify → stop → remove
- [x] T066 [US5] Create .github/workflows/release.yml: goreleaser + cosign signing + SLSA provenance on git tag

**Checkpoint**: US5 complete. CI/CD pipeline runs end-to-end. Official GitHub Action available.

---

## Phase 8: User Story 6 — Set Up a Private Network (P2)

**Goal**: `trond network create --intent private-network.yaml` starts a multi-node private TRON network

**Independent Test**: Create network → witness produces blocks → fullnodes sync → destroy cleans up

### Implementation for User Story 6

- [ ] T067 [US6] Implement cmd/network/create.go: `trond network create --intent <file>` — iterate over nodes[], deploy each, wire peer connections via seed.node config
- [ ] T068 [US6] Implement cmd/network/status.go: `trond network status` — aggregate status of all nodes in the intent
- [ ] T069 [US6] Implement cmd/network/destroy.go: `trond network destroy --confirm <name>` — stop + remove all nodes in the network
- [ ] T070 [US6] Enhance internal/render/hocon.go to inject seed.node addresses from multi-node intent (witness IP into fullnode config)

**Checkpoint**: US6 complete. Private dev network up/down in one command.

---

## Phase 9: User Story 7 — Upgrade a Running Node (P3)

**Goal**: `trond upgrade <node> --version 4.8.2` performs safe version upgrade with automatic rollback on failure

**Independent Test**: Deploy at version X → upgrade to Y → verify new version running → force-fail → verify rollback

### Implementation for User Story 7

- [ ] T071 [US7] Implement cmd/upgrade.go: `trond upgrade <node> --version <ver>` — download new jar/pull new image → stop → replace → start → verify → rollback on failure
- [ ] T072 [US7] Implement cmd/rollback.go: `trond rollback <node>` — restore previous_version from state, redeploy
- [ ] T073 [US7] Extend internal/state/types.go: store previous_version on upgrade for rollback support

**Checkpoint**: US7 complete. Safe upgrade with rollback works.

---

## Phase 10: User Story 8 — AI Agent Automated Deployment (P3)

**Goal**: An AI agent reads CLAUDE.md and can complete an end-to-end deployment without external docs

**Independent Test**: Give Claude Code access to the repo; ask it to deploy a fullnode; verify it succeeds using only CLAUDE.md and --help

### Implementation for User Story 8

- [ ] T074 [US8] Write comprehensive CLAUDE.md: command reference, workflow guide, error handling guide, example conversations
- [ ] T075 [P] [US8] Create knowledge/node-types.md: fullnode vs witness vs solidity vs lite — when to use each, resource requirements
- [ ] T076 [P] [US8] Create knowledge/troubleshooting.md: common issues (sync lag, peer starvation, disk full, port conflict) with structured diagnostic steps
- [ ] T077 [P] [US8] Create knowledge/best-practices.md: production deployment checklist, JVM tuning guide, monitoring recommendations
- [ ] T078 [P] [US8] Create knowledge/cloud-deployment.md: AWS/GCP/Azure workflow (Terraform → SSH → trond), architecture diagrams
- [ ] T079 [P] [US8] Create knowledge/config-reference.md: important HOCON config keys with explanations
- [ ] T080 [US8] Implement internal/knowledge/embed.go: use go:embed to bundle knowledge/*.md into the binary
- [ ] T081 [US8] Implement cmd/knowledge.go: `trond knowledge <topic>` — list topics or display specific topic content
- [ ] T082 [US8] Implement cmd/config/explain.go: `trond config explain <key>` — look up HOCON key in knowledge base, return explanation

**Checkpoint**: US8 complete. AI agents can self-serve deployment using only CLAUDE.md + trond --help + trond knowledge.

---

## Phase 11: Polish & Cross-Cutting Concerns

**Purpose**: Documentation, security hardening, release readiness

- [ ] T083 [P] Rewrite README.md: new project description, install instructions, quickstart, architecture diagram, trust ladder, comparison with start.sh
- [ ] T084 [P] Create SECURITY.md: vulnerability disclosure policy, reporting channel, response SLAs, bounty program link
- [ ] T085 [P] Create cmd/restart.go: `trond restart <node>` — stop + start convenience command
- [ ] T086 Implement --log-format json support in all commands via internal/output structured logger
- [ ] T087 Add audit log writes to all mutating commands (apply, stop, start, remove, upgrade, rollback, network create/destroy)
- [ ] T088 Run golangci-lint on entire codebase and fix all findings
- [ ] T089 Run `trond config validate` against all example intent files to verify they pass
- [ ] T090 Validate quickstart.md scenarios end-to-end
- [ ] T091 Tag v0.1.0 release and verify goreleaser + cosign pipeline
      (was `v0.1.0-alpha`; the phased alpha plan further down was never
      followed — everything landed on develop untagged and ships as one
      `v0.1.0`. `prerelease: auto` would also file any `-alpha` tag as a
      pre-release, which `scripts/install.sh` does not resolve.)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: Depends on Phase 1 — BLOCKS all user stories
- **Phase 3 (US1)**: Depends on Phase 2 — MVP target
- **Phase 4 (US2)**: Depends on Phase 2 — can parallel with US1
- **Phase 5 (US3)**: Depends on Phase 2 — can parallel with US1/US2
- **Phase 6 (US4)**: Depends on Phase 3 or 4 (needs a deployed node to diagnose)
- **Phase 7 (US5)**: Depends on Phase 3 (needs working apply/verify)
- **Phase 8 (US6)**: Depends on Phase 3 (needs working apply for multi-node)
- **Phase 9 (US7)**: Depends on Phase 3 (needs working apply + state tracking)
- **Phase 10 (US8)**: Depends on Phase 6 (needs diagnose + knowledge)
- **Phase 11 (Polish)**: Depends on all desired phases being complete

### User Story Dependencies

```
Phase 1 (Setup)
    │
Phase 2 (Foundational)
    │
    ├── US1 (Docker fullnode) ──────┐
    ├── US2 (SSH jar witness) ──────┤── can run in parallel
    └── US3 (Plan/apply) ──────────┘
         │
         ├── US4 (Status/diagnose) ── needs a deployed node
         ├── US5 (CI pipeline) ────── needs working apply
         ├── US6 (Private network) ── needs working apply
         └── US7 (Upgrade) ────────── needs working apply + state
              │
              US8 (AI agent) ────────── needs diagnose + knowledge
```

### Parallel Opportunities

**Within Phase 2** (largest parallelization benefit):
- T010/T011/T012 (output pkg) — parallel
- T013/T014 (security/privatekey) — parallel with output
- T020/T021 (compose/systemd renderers) — parallel
- T023/T024 (local/ssh targets) — parallel
- T026/T027 (docker/jar runtimes) — parallel

**Across P1 stories** (after Phase 2):
- US1 (local docker) and US2 (remote jar) can be implemented simultaneously
- US3 (plan) can parallel with US1/US2

---

## Implementation Strategy

### MVP First (US1 Only)

1. Complete Phase 1: Setup (~9 tasks)
2. Complete Phase 2: Foundational (~23 tasks)
3. Complete Phase 3: US1 — Local Docker Fullnode (~9 tasks)
4. **STOP and VALIDATE**: `trond apply` → running fullnode → `trond status` → `trond stop`
5. Tag v0.1.0-alpha

### Incremental Delivery

1. v0.1.0-alpha: US1 (local docker fullnode)
2. v0.2.0-alpha: + US2 (SSH jar witness) + US3 (plan/apply)
3. v0.3.0-alpha: + US4 (diagnose) + US5 (CI pipeline)
4. v0.4.0-alpha: + US6 (private network) + US7 (upgrade)
5. v0.9.0-beta: + US8 (AI/knowledge) + security audit prep
6. v1.0.0: Third-party audit complete, SLSA L3, production-ready for fullnodes

---

## Notes

- [P] tasks = different files, no dependencies
- [Story] label maps task to specific user story
- Each user story is independently completable and testable
- Commit after each task or logical group
- Stop at any checkpoint to validate story independently
- Total: 91 tasks across 11 phases

# Feature Specification: trond CLI Deployment Platform

**Feature Branch**: `001-trond-cli-platform`
**Created**: 2026-04-08
**Status**: Draft
**Input**: User description: "Redesign tron-deployment as a CLI-first, AI-friendly java-tron node deployment platform with trond CLI"

## Clarifications

### Session 2026-04-08

- Q: How should trond-managed nodes be uniquely identified? → A: User-assigned `name` field in intent.yaml, required and unique per state file.
- Q: What is the relationship between `deploy` and `apply` commands? → A: `deploy` is an alias for `apply`; both are idempotent. `apply` is the canonical command.
- Q: How should trond handle a previously interrupted (partial) deployment? → A: `apply` steps are individually idempotent (check-then-act). This recovery promise applies to `apply` only; `upgrade` and `rollback` use their artifact transaction and do not promise automatic no-human recovery.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Deploy a Mainnet Fullnode Locally via Docker (Priority: P1)

A node operator wants to quickly spin up a java-tron mainnet fullnode on their local
machine for development or initial testing. They write a minimal intent file (or use an
example), run a single `trond deploy` command, and receive a running fullnode with
structured JSON output showing endpoints and sync progress.

**Why this priority**: This is the most basic end-to-end path. If this works, the entire
render → deploy → status pipeline is proven. Every subsequent story builds on this one.

**Independent Test**: Can be fully tested by running `trond deploy --intent examples/mainnet-fullnode.yaml --output json` on a machine with Docker installed. Delivers a running fullnode container and a parseable JSON response with API endpoints.

**Acceptance Scenarios**:

1. **Given** a machine with Docker installed and an intent file specifying `network: mainnet, node_type: fullnode, runtime: docker, target: local`, **When** the user runs `trond deploy --intent <file>`, **Then** trond renders a valid HOCON config from the base `main_net_config.conf` template plus intent overrides, starts a container, and outputs JSON containing `status: "running"`, container ID, and HTTP/gRPC endpoints.
2. **Given** the same intent is applied a second time with no changes, **When** the user runs `trond apply --intent <file>`, **Then** trond detects no diff and exits with code 0 and `changes: []`.
3. **Given** an intent file with an invalid field (e.g., unknown `network: foobar`), **When** the user runs `trond config validate <file>`, **Then** trond exits with code 2 and outputs a JSON error with `code`, `message`, and `suggestions`.

---

### User Story 2 - Deploy a Witness Node on a Remote Server via Jar (Priority: P1)

An SR operator wants to deploy a java-tron witness (block-producing) node on a remote
Linux server using the jar runtime and systemd process management. The private key MUST
be injected via an environment variable, never written to disk.

**Why this priority**: This is the production SR use case — the most security-sensitive
and economically significant deployment scenario. Supporting it in V1 establishes trust
with the most demanding user segment.

**Independent Test**: Can be tested by deploying to an SSH-reachable server with JDK installed. The witness node starts, connects to mainnet peers, and begins block production. Private key is verified to exist only in the systemd environment, not in any file.

**Acceptance Scenarios**:

1. **Given** a remote server reachable via SSH with JDK 8+ and systemd, and an intent file specifying `target: ssh, runtime: jar, node_type: witness`, **When** the user runs `SR_PRIVATE_KEY=<key> trond deploy --intent <file>`, **Then** trond downloads the specified java-tron jar (with SHA256 verification), renders HOCON config, generates a systemd unit file, and starts the service. Output JSON includes `status: "running"` and the systemd unit name.
2. **Given** the witness node is running, **When** the user runs `trond status <node> --output json`, **Then** output includes current block height, sync status, peer count, and whether the node is actively producing blocks.
3. **Given** the intent file contains `witness_key_env: SR_PRIVATE_KEY` and the environment variable is unset, **When** the user runs `trond deploy`, **Then** trond exits with code 2 and an error message explaining the missing key, with a suggestion to set the environment variable.

---

### User Story 3 - Preview Deployment Changes Before Applying (Priority: P1)

A DevOps engineer wants to review what trond will change before actually deploying. They
run `trond plan` which outputs a structured diff — similar to `terraform plan` — that can
be posted as a PR comment in their CI pipeline.

**Why this priority**: Plan/apply separation is fundamental to pipeline safety. Without it,
trond cannot be trusted in automated workflows.

**Independent Test**: Can be tested by running `trond plan --intent <file> --output json` against an existing deployment with a modified intent. The output shows the exact changes without modifying anything on the target.

**Acceptance Scenarios**:

1. **Given** a fullnode deployed with version 4.8.0 and an intent file updated to version 4.8.1, **When** the user runs `trond plan --intent <file> --output json`, **Then** output includes a `changes[]` array with entries like `{"type": "version_upgrade", "from": "4.8.0", "to": "4.8.1", "restart_required": true}`, plus a `destructive: false` flag and estimated downtime.
2. **Given** a plan that includes destructive changes, **When** the user runs `trond apply` without `--auto-approve`, **Then** trond prints the plan summary and prompts for confirmation.
3. **Given** a CI pipeline, **When** the pipeline runs `trond plan --output json`, **Then** the JSON can be directly used by a GitHub Action step to comment on the PR.

---

### User Story 4 - Check Node Status and Diagnose Issues (Priority: P2)

An operator (or an AI agent via Claude Code) wants to check the health of a running node
and, if problems exist, get structured diagnostic information with repair suggestions.

**Why this priority**: Observability and diagnosis are key differentiators vs. manual SSH.
This is the main reason operators and AI agents would adopt trond over raw Docker/systemd
commands.

**Independent Test**: Can be tested by deploying a node, then running `trond diagnose <node> --output json`. Delivers a structured report with checks passed/failed and actionable suggestions.

**Acceptance Scenarios**:

1. **Given** a running fullnode, **When** the user runs `trond status <node> --output json`, **Then** output includes `block_height`, `sync_progress_percent`, `peer_count`, `is_synced`, `runtime` (docker/jar), `uptime`, and `api_endpoints`.
2. **Given** a node that has fallen behind by more than 100 blocks, **When** the user runs `trond diagnose <node> --output json`, **Then** output includes a `checks[]` array where the sync check shows `status: "warning"` and `suggestions` includes "Check network connectivity and peer count".
3. **Given** a node that is stopped, **When** the user runs `trond status <node>`, **Then** output shows `status: "stopped"` and the last known block height.

---

### User Story 5 - Deploy via CI/CD Pipeline (Priority: P2)

A team uses GitHub Actions to deploy java-tron nodes. Intent files live in a `deployments/`
directory in git. When a PR modifying an intent file is merged to main, the pipeline
validates, plans, deploys, and verifies — all through trond commands.

**Why this priority**: Pipeline integration is a core differentiator and aligns with the
Constitution's "Pipeline as First-Class Citizen" principle.

**Independent Test**: Can be tested by creating a GitHub Actions workflow that runs `trond config validate`, `trond plan`, `trond apply --auto-approve`, and `trond verify` in sequence, using the `tronprotocol/setup-trond` action for installation.

**Acceptance Scenarios**:

1. **Given** a GitHub Actions workflow using `tronprotocol/setup-trond@v1`, **When** the action runs, **Then** the correct trond binary is downloaded, signature-verified, and added to PATH in under 10 seconds.
2. **Given** a pipeline that runs `trond apply --auto-approve --intent <file> --output json`, **When** deployment succeeds, **Then** exit code is 0 and output JSON is parseable by subsequent pipeline steps.
3. **Given** a pipeline that runs `trond verify --intent <file> --timeout 10m`, **When** the node fails health checks, **Then** exit code is non-zero and the pipeline triggers the rollback step.

---

### User Story 6 - Set Up a Private Network for Development (Priority: P2)

A developer wants to quickly spin up a complete private TRON network (1 witness + 2
fullnodes) on their local machine for smart contract testing and development.

**Why this priority**: Private networks are the primary development environment. Making
this a single-command experience is a major usability win.

**Independent Test**: Can be tested by running `trond network create --intent examples/private-network.yaml`. Delivers a running private network with blocks being produced every 3 seconds.

**Acceptance Scenarios**:

1. **Given** Docker is installed and an intent file defines a private network with 1 witness and 2 fullnodes, **When** the user runs `trond network create --intent <file>`, **Then** all three nodes start, the witness begins producing blocks, and fullnodes sync from the witness.
2. **Given** a running private network, **When** the user runs `trond network status --output json`, **Then** output includes each node's role, block height, peer connections, and overall network health.
3. **Given** the user is done testing, **When** they run `trond network destroy --confirm private-dev`, **Then** all containers are stopped and removed, with data optionally preserved via `--keep-data`.

---

### User Story 7 - Upgrade a Running Node (Priority: P3)

An operator wants to upgrade a running java-tron node to a new version with minimal
downtime and the ability to roll back if the upgrade fails.

**Why this priority**: Version upgrades are routine operations but carry risk. Safe upgrade
with rollback capability is important for production but can follow after core deploy/status
functionality is stable.

**Independent Test**: Can be tested by deploying a node at version X, then running `trond upgrade <node> --version Y`. The node restarts with the new version; health verification is separate or supplied by the `upgrade-with-verify` recipe.

**Acceptance Scenarios**:

1. **Given** a fullnode running version 4.8.0, **When** the user runs `trond upgrade <node> --version 4.8.1`, **Then** trond downloads the new jar/image, stops the node, replaces the binary/image, and restarts. Bare `trond upgrade` does not verify health.
2. **Given** an upgrade that must be health-gated, **When** the `upgrade-with-verify` recipe (or a network upgrade) fails verification, **Then** that workflow rolls back and reports diagnostics.
3. **Given** a successful upgrade, **When** the user later runs `trond rollback <node>`, **Then** the previous version is restored.

---

### User Story 8 - AI Agent Automated Deployment (Priority: P3)

A user asks an AI coding agent (Claude Code, Cursor, etc.) to "deploy a mainnet fullnode
on my AWS server". The AI reads the CLAUDE.md file, discovers trond, and completes the
entire workflow — selecting the right example intent, customizing it, running preflight
checks, deploying, and reporting results — all through CLI commands.

**Why this priority**: AI-driven deployment is the project's north-star use case but
depends on all lower-level stories working correctly first.

**Independent Test**: Can be tested by giving an AI agent access to a machine with trond installed and observing whether it can complete an end-to-end deployment using only information from CLAUDE.md and `trond --help`.

**Acceptance Scenarios**:

1. **Given** a fresh Claude Code session in a directory containing CLAUDE.md, **When** the user asks "deploy a mainnet fullnode locally", **Then** the AI reads CLAUDE.md, runs `trond config validate` then `trond deploy --intent examples/mainnet-fullnode.yaml --output json`, and reports the endpoints to the user.
2. **Given** the deployed node shows sync issues, **When** the user asks "what's wrong with my node?", **Then** the AI runs `trond diagnose <node> --output json`, reads the suggestions, and either fixes the issue or explains it to the user.
3. **Given** an AI agent that does not have prior knowledge of trond, **When** it reads CLAUDE.md, **Then** the document contains enough information (command list, example workflows, error handling guidance) for the agent to operate trond correctly without external documentation.

---

### Edge Cases

- What happens when Docker is not installed and target is local with runtime docker? trond MUST report a clear error with installation instructions.
- What happens when SSH connection fails during a remote deployment? trond MUST report the SSH error with suggestions (check host, key, firewall) and exit with code 3.
- What happens when a node's data directory already contains data from a different network? trond MUST detect the mismatch and refuse to deploy without `--force`.
- What happens when the specified java-tron version does not exist on GitHub Releases? trond MUST report the version as unavailable and list the latest 5 available versions.
- What happens when disk space is insufficient for a fullnode? `trond preflight` MUST detect this and warn before deployment begins.
- What happens when two users run `trond apply` simultaneously on the same target? The locking mechanism MUST ensure only one proceeds; the other receives a lock-conflict error.
- What happens when the user's intent.yaml references a witness private key env var that contains a malformed key? trond MUST validate key format before deployment and report a clear error without echoing the key value.
- What happens when trond is interrupted mid-deployment? For `trond apply`, each step MUST check-then-act and converge without manual cleanup. This guarantee does not extend to bare `upgrade` or `rollback`.
- What happens when two intent files define the same node `name`? trond MUST reject the second `apply` with a conflict error referencing the existing node's intent source.

## Requirements *(mandatory)*

### Functional Requirements

**Configuration & Validation**:

- **FR-001**: trond MUST validate intent files against a published schema before any deployment action. Each intent MUST include a `name` field that uniquely identifies the node within the trond state file.
- **FR-002**: trond MUST render intent files into final java-tron HOCON configuration by overlaying intent-driven changes on base templates (`main_net_config.conf`, `test_net_config.conf`, `private_net_config.conf`).
- **FR-003**: trond MUST generate runtime-specific deployment artifacts: `docker-compose.yaml` for docker runtime; systemd unit files for jar runtime.
- **FR-004**: trond MUST support a `config render` command that outputs generated artifacts without deploying (dry-run for configuration).
- **FR-005**: trond MUST support a `config diff` command that shows differences between an intent and the currently deployed configuration.

**Deployment & Lifecycle**:

- **FR-006**: trond MUST support deploying java-tron nodes to `local` and `ssh` targets.
- **FR-007**: trond MUST support both `docker` and `jar` runtimes in V1.
- **FR-008**: For docker runtime, trond MUST use `docker compose` to manage containers, consuming tron-docker image references.
- **FR-009**: For jar runtime, trond MUST download the java-tron jar from official GitHub Releases with SHA256 verification.
- **FR-010**: For jar runtime, trond MUST generate systemd unit files and manage the node via `systemctl`.
- **FR-011**: trond MUST support `apply`, `start`, `stop`, `restart`, and `remove` lifecycle commands for individual nodes. `deploy` is an alias for `apply` and behaves identically.
- **FR-012**: `apply` is the canonical deployment command and MUST be idempotent: create if not exists, update if changed, no-op if identical. Each apply step MUST independently verify its precondition. This check-then-act/no-manual-recovery promise is specific to `apply`, not bare `upgrade` or `rollback`.
- **FR-013**: trond MUST support `plan` to preview changes without applying them.
- **FR-014**: Destructive operations (`remove --purge`, `rollback`) MUST default to dry-run and require explicit `--auto-approve` or `--confirm <node-name>`.

**Observability & Diagnosis**:

- **FR-015**: trond MUST provide a `status` command returning node state, block height, sync progress, peer count, and API endpoints.
- **FR-016**: trond MUST provide a `logs` command supporting `--tail` and `--follow` flags, abstracting docker logs vs journalctl.
- **FR-017**: trond MUST provide a `health` command performing endpoint probing and sync-progress validation.
- **FR-018**: trond MUST provide a `diagnose` command returning structured checks with pass/fail status and actionable repair suggestions.
- **FR-019**: trond MUST provide a `verify` command that acts as a post-deployment health gate, suitable for CI pipelines.

**Multi-Node & Network**:

- **FR-020**: trond MUST support `network create` to deploy a complete private network (N witnesses + M fullnodes) from a single intent file.
- **FR-021**: trond MUST support `network status` and `network destroy` for managing private networks.

**Upgrade & Rollback**:

- **FR-022**: trond MUST support `upgrade` to update a node to a new java-tron version. Bare upgrade does not verify health; health-gated upgrade is provided by the `upgrade-with-verify` recipe and `network upgrade`.
- **FR-023**: trond MUST support `rollback` to revert to the previous version.

## Implementation Contracts

- Intent hash and seven-port restoration are single-sourced in
  `internal/apply/hash.go:14-74`; CLI apply/plan and MCP plan/apply use its
  effective, legacy-match, and auto-port functions.
- `internal/state/store.go:64-113` uses a `0600` temp file, file sync, atomic
  rename, and directory sync; save failures are `STATE_ERROR`, and state.lock
  protects read-modify-write from lost updates.
- `internal/runtime/runtime.go:97-107` defines prepare/activate/start/rollback/
  cleanup artifact transactions. Version state commits only after activation
  and start succeed; jar disk, pin, and recorded version must converge.
- `internal/target/target.go:26-107` owns pooled transports and timeout
  dialing. StreamExec covers cancellation, explicit close, and natural EOF.
- `cmd/resolve.go:127-146` gives write commands exclusive locking with a
  30-second `LOCK_TIMEOUT`; read-only commands do not take that write lock.
- `internal/render/hocon.go:145-172` parse-first redaction protects plan/config
  diffs, verify-config, MCP resources, and MCP drift output; internal drift
  comparison retains original bytes.

**Preflight & Bootstrap**:

- **FR-024**: trond MUST provide a `preflight` command that checks target readiness (Docker/JDK installed, ports available, disk space, memory).
- **FR-025**: trond MUST provide a `bootstrap` command that installs Docker or JDK on a target.

**Output & Integration**:

- **FR-026**: Every trond command MUST support `--output json` for machine-readable output.
- **FR-027**: Error output MUST be structured JSON containing `code`, `message`, and `suggestions[]` fields.
- **FR-028**: Exit codes MUST follow a documented, stable semantic table.
- **FR-029**: trond MUST support `--log-format json` for structured logging to stdout/stderr.
- **FR-030**: All operations MUST be recorded to a local audit log.

**Security**:

- **FR-031**: Private keys MUST only be accepted via environment variable references in intent files, never as plaintext values.
- **FR-032**: Private key values MUST never appear in log output, error messages, rendered config files, or audit logs.
- **FR-033**: Remote SSH commands MUST use a whitelisted command set; arbitrary shell execution from intent fields MUST be impossible.

**Knowledge & AI**:

- **FR-034**: trond MUST provide a `knowledge` subcommand that retrieves deployment guidance from bundled markdown files (node types, troubleshooting, best practices).
- **FR-035**: A CLAUDE.md file MUST exist in the repository root, teaching AI agents how to discover and use trond.

### Key Entities

- **Intent**: A declarative YAML file describing the desired state of one or more nodes. Contains a required `name` field, target, runtime, network, node type, features, and resource specifications.
- **Node**: A managed java-tron instance tracked by trond. Uniquely identified by the user-assigned `name` field from the intent file. Name MUST be unique within a state file. Has a target, runtime, network, current version, and state (running/stopped/error).
- **Target**: The destination where a node is deployed. Characterized by type (local/ssh) and connection parameters.
- **Runtime**: The execution environment for a node (docker or jar), determining how processes are managed.
- **Base Template**: One of the three existing `.conf` files used as the foundation for configuration rendering.
- **State**: A local (or remote) record of all trond-managed nodes, their current configuration hashes, and deployed versions.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A user with Docker installed can go from zero to a running mainnet fullnode in under 5 minutes using `trond deploy` with an example intent file.
- **SC-002**: An SR operator can deploy a production witness node to a remote server via SSH, with private key never touching disk, in under 10 minutes.
- **SC-003**: `trond apply` is fully idempotent: running it 10 times on an unchanged intent produces zero changes and zero downtime.
- **SC-004**: An AI agent (Claude Code) with no prior trond knowledge can read CLAUDE.md and successfully complete an end-to-end local deployment on first attempt.
- **SC-005**: All commands complete with structured JSON output that can be parsed by `jq` without errors.
- **SC-006**: CI/CD pipeline deployment (validate → plan → apply → verify) completes end-to-end with zero manual intervention.
- **SC-007**: Node diagnosis reports identify at least 80% of common operational issues (sync lag, peer starvation, disk full, port conflict, version mismatch) with actionable suggestions.
- **SC-008**: A private TRON network (1 witness + 2 fullnodes) can be created and destroyed from a single intent file in under 3 minutes on a local Docker setup.
- **SC-009**: trond binary installs in CI environments in under 10 seconds via `tronprotocol/setup-trond` GitHub Action or direct curl download.
- **SC-010**: All release binaries are verifiable via `cosign verify-blob` against the public Rekor transparency log.

## Assumptions

- Target machines for SSH deployment have a standard Linux environment (Ubuntu 20.04+ or similar) with `systemd` as the init system.
- Docker targets have Docker Engine 20.10+ and Docker Compose v2 installed.
- Jar runtime targets have JDK 8 (or JDK 17 for arm64) pre-installed, or `trond bootstrap` is run first.
- Users deploying to remote targets have SSH key-based access configured.
- The existing `main_net_config.conf`, `test_net_config.conf`, and `private_net_config.conf` files are kept in sync with upstream java-tron by the maintainers.
- tron-docker's Docker images (`tronprotocol/java-tron`) and compose templates remain available and maintained.
- The java-tron project continues to publish versioned jar files on GitHub Releases with SHA256 checksums.
- State backend in V1 is local file only (`~/.trond/state.json`); remote state backends (S3, GCS) are deferred to V2.
- Kubernetes target support is deferred to V3.
- Web UI is out of scope indefinitely.
- trond does not provision cloud infrastructure (VMs, networks, security groups); that is the user's or Terraform's responsibility.

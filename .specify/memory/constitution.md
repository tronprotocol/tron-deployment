<!--
Sync Impact Report
==================
Version change: (none) → 1.0.0
Bump rationale: Initial ratification of the project constitution. The repository is being
repositioned from a passive HOCON configuration template repository into a CLI-first,
AI-friendly java-tron deployment platform; this constitution establishes the founding
principles and non-negotiable constraints for that redesign.

Modified principles: N/A (initial draft)
Added sections:
  - Core Principles (8 principles)
  - Boundaries (Out of Scope)
  - Security & Supply Chain Requirements
  - Development Workflow & Release Discipline
  - Governance

Removed sections: None (template placeholders replaced).

Templates requiring updates:
  - .specify/templates/plan-template.md            ⚠ pending review for Constitution Check alignment
  - .specify/templates/spec-template.md            ⚠ pending review for scope/requirements alignment
  - .specify/templates/tasks-template.md           ⚠ pending review for principle-driven task categories
  - .specify/templates/commands/*.md               ⚠ pending review for outdated agent-specific references
  - README.md                                      ⚠ pending rewrite to reflect new project scope
  - CLAUDE.md                                      ⚠ to be created (AI-agent entry guide)

Follow-up TODOs:
  - TODO(README_REWRITE): Rewrite README to describe trond CLI, intent.yaml workflow, and
    relationship with java-tron / tron-docker / start.sh.
  - TODO(CLAUDE_MD): Author a top-level CLAUDE.md that teaches AI agents how to invoke trond.
  - TODO(TRON_DOCKER_RFC): Draft cross-repository RFC for merging tron-docker tools/trond/ into
    this repository.
-->

# tron-deployment Constitution

## Core Principles

### I. CLI-First, AI-Friendly (NON-NEGOTIABLE)

The sole user-facing deliverable of this project is a single CLI binary named `trond`.
All capabilities — configuration rendering, deployment, lifecycle, observability, diagnosis —
MUST be accessible through `trond` subcommands. No GUI, no web console, no required SDK in
V1.

Every command MUST support `--output json` for machine-readable output. Errors MUST be
emitted as JSON objects containing at minimum `code`, `message`, and `suggestions[]` fields.
Exit codes MUST be semantically defined and stable across releases so that CI pipelines and
AI agents can branch on them deterministically.

A top-level `CLAUDE.md` (and equivalents for other coding agents as they emerge) MUST exist
in the repository root so any AI agent learns how to use `trond` from a single read at
session start. MCP server support is explicitly out of scope for V1; if added later it MUST
be a thin wrapper over the same CLI, not a parallel implementation.

**Rationale**: A CLI is universally consumable — by humans, by Claude Code via the Bash
tool, by Cursor / Aider / Codex / shell scripts / CI pipelines — without protocol or SDK
lockin. Locking to MCP would limit reach for no proportionate benefit at the project's
current stage.

### II. Declarative Intent

Users MUST describe what they want via an `intent.yaml` file; they MUST NOT hand-edit raw
HOCON for routine deployments. The intent file is the source of truth and is intended to
live in git for GitOps workflows.

The deployment pipeline MUST follow `intent.yaml → renderer → final java-tron HOCON +
runtime artifacts (docker-compose.yml or systemd unit)`. Renderers MUST be deterministic
and idempotent: the same intent on the same target produces byte-identical artifacts.

Existing files `main_net_config.conf`, `test_net_config.conf`, and `private_net_config.conf`
MUST be preserved as base templates. The renderer applies intent-driven overlays on top of
these bases rather than generating configuration from scratch, ensuring continued sync with
upstream java-tron defaults.

**Rationale**: Declarative intent + git is the only model that scales to multiple
environments, multi-person teams, AI-driven automation, audit trails, and reproducible
disaster recovery. Hand-edited HOCON does not.

### III. Reuse Over Reinvention

This project MUST integrate with, not duplicate, existing TRON ecosystem assets:

- **tron-docker**: trond MUST consume tron-docker's Dockerfiles, compose templates,
  monitoring stack, and toolkit utilities. trond MUST NOT rewrite container assets.
- **java-tron `start.sh`**: trond MUST NOT depend on, call, or attempt to replace `start.sh`.
  trond MAY port algorithms from it (JVM tuning, jar downloading, graceful shutdown) into
  internal modules with unit tests. The `jar` runtime's on-disk layout MUST remain
  compatible with `start.sh` so operators can fall back manually.
- **Cloud provider SDKs (AWS / GCP / Azure / Aliyun / etc.)**: trond MUST NOT embed any
  cloud SDK. All cloud targets MUST be reached through the unified SSH / Docker context
  abstraction. Provisioning of cloud resources is the responsibility of Terraform or the
  user's chosen IaC tool.
- **systemd / supervisord**: trond MUST NOT replace these process managers; it generates
  configuration for them.
- **Prometheus / Grafana / Loki**: trond MUST NOT build a parallel observability stack;
  the `monitor enable` capability wires up tron-docker's existing compose definitions.

**Rationale**: Every reinvention is a maintenance liability. Each existing asset already has
maintainers; staying composable preserves their value and keeps trond's surface small.

### IV. Multi-Runtime and Multi-Target Abstraction

trond MUST treat **runtime** and **target** as orthogonal first-class abstractions in the
intent schema and command surface.

**Runtimes** (V1 MUST support both):

- `docker` (default): java-tron runs as a container managed via `docker compose`.
- `jar`: java-tron runs as a host process managed by `systemd`, with the jar downloaded and
  verified by trond. This runtime is non-negotiable for production SR/witness operators
  who require fine-grained JVM tuning and minimal isolation overhead.

**Targets** (V1 MUST support `local` and `ssh`):

- `local`: the host running trond.
- `ssh://user@host`: any remote machine reachable over SSH; this single abstraction MUST
  cover every cloud provider.
- `docker-context`: V2.
- `kubernetes`: V3.

All commands (`deploy`, `status`, `logs`, `stop`, `upgrade`, `diagnose`, etc.) MUST be
transparent to the chosen runtime and target combination.

**Rationale**: Production node operators run heterogeneous infrastructure. A single CLI
that handles both containerized and bare-metal deployments through a uniform interface is
the only way to consolidate the operational toolchain.

### V. Pipeline as a First-Class Citizen

CI/CD pipeline support is not an afterthought — it dictates the design of every command.
trond MUST satisfy the following pipeline requirements:

- **Idempotency**: `trond apply` MUST be safe to re-run any number of times. If state
  matches intent, no changes are applied; otherwise the minimal converging diff is applied.
- **Plan / Apply separation**: `trond plan` MUST output a structured diff between current
  and desired state, with a `destructive` flag and `estimated_downtime_seconds`. CI MUST be
  able to comment plan output on pull requests for human review.
- **Semantically defined exit codes**: A documented, stable exit-code table covering
  success, no-op, validation error, target unreachable, preflight failure, partial
  success, and "human intervention required".
- **Structured logging**: `--log-format json` MUST be available for log aggregation.
- **State backend**: Local file by default; remote backends (S3, GCS) and locks
  (DynamoDB, etcd, Consul) MUST be supported by V2.
- **Verification and rollback**: `trond verify` (post-deploy health gate) and
  `trond rollback` MUST exist as first-class commands.
- **Official integrations**: A `tronprotocol/setup-trond` GitHub Action MUST be published
  alongside the V1 release. trond MUST be installable via curl from GitHub Releases and
  via a published Docker image.
- **Interoperability with Terraform**: trond MUST be able to consume Terraform JSON output
  to populate target fields, but MUST NOT itself manage cloud resources.

**Rationale**: Cloud deployments increasingly happen through pipelines, not human shells.
A tool that is awkward in CI is effectively excluded from production use.

### VI. Static Binary Distribution

The implementation language for trond V1 is **Go**. This decision is binding because:

- Static single-file binaries are mandatory for pipeline portability and the existence of
  `tron-docker/tools/trond/` (already in Go) provides a starting point.
- Cross-compilation for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` MUST
  be a single CI step.
- Trond MUST follow strict semantic versioning. Breaking changes require a major version
  bump and a documented migration path.
- Pipelines MUST pin trond to an explicit version. Use of `latest` in CI is forbidden in
  documentation and examples.
- Distribution channel for V1: GitHub Releases ONLY — cross-compiled binaries and
  .deb/.rpm/.apk packages, hashed into `checksums.txt`, which is signed with cosign
  keyless OIDC. `scripts/install.sh` consumes that release and verifies the hash.
  Channels needing a standing publish credential (a Homebrew tap, a container
  registry) are deliberately excluded: that credential could publish under this
  project's name until someone rotated it, and its artifacts cannot appear in the
  `checksums.txt` the signature covers. Adding one is a V2 decision that MUST come
  with a named rotation owner and a verification story of its own.
- Offline / air-gapped environments MUST be supported via the `TROND_DOWNLOAD_MIRROR`
  environment variable, which redirects all of trond's external downloads (jars, snapshots,
  bootstrap dependencies) to a user-supplied internal mirror.

**Rationale**: Pipeline-first distribution rules out interpreter-based languages. Go is
the only choice that simultaneously satisfies the static-binary requirement and aligns
with existing TRON ecosystem code.

### VII. Verifiable Security (NON-NEGOTIABLE)

Trust in trond MUST NOT rest on assertions by maintainers. Every claim MUST be
independently verifiable by any user.

**Supply chain**:

- All release binaries MUST be signed via Sigstore / cosign, with signatures recorded in
  the public Rekor transparency log.
- Releases MUST satisfy SLSA Build Level 3 or higher; provenance MUST be published
  alongside binaries.
- Builds MUST be reproducible: third parties MUST be able to rebuild any tagged release
  from public source and obtain a byte-identical artifact.
- Each release page MUST publish SHA256, cosign signature, and SLSA provenance together.

**Code integrity**:

- The repository MUST be 100% open source and hosted under the `tronprotocol` GitHub
  organization.
- All changes MUST go through pull requests with at least one independent review; security-
  sensitive modules require two reviewers via CODEOWNERS.
- CI MUST run CodeQL, gosec, govulncheck, staticcheck, and OSSF Scorecard on every PR;
  failing scans block merge.
- A third-party security audit MUST be completed before any 1.0.0 release tagged "stable".

**Runtime safety**:

- Private keys MUST be represented by a dedicated Go type whose `String()`, `MarshalJSON`,
  and error formatters return `[REDACTED]`. This MUST be enforced by unit tests.
- Private keys MUST NEVER be written to disk and MUST NEVER appear in logs at any verbosity
  level. Keys are injected into target processes only via environment variables, which are
  cleared as soon as the child process exits.
- Destructive operations (`remove`, `rollback`, `restore`, etc.) MUST default to dry-run
  and require explicit `--auto-approve` (for CI) or `--confirm <node-name>` (for
  interactive use).
- All operations MUST be recorded to a local audit log (`~/.trond/audit.log`) with optional
  forwarding to a user-specified webhook.
- Remote SSH execution MUST use a whitelisted command set; arbitrary shell strings MUST NOT
  be constructable from intent fields.

**Disclosure**:

- A `SECURITY.md` MUST define the vulnerability reporting channel, response SLAs, and
  reward program (linked to the TRON mainnet bounty program where applicable).
- A quarterly transparency report MUST cover security issues processed, CVEs affecting
  trond, and any anomalous release-pipeline activity.

**User-facing verification**:

- The `README.md` MUST contain a "Verify trond before use" checklist showing users how to
  run `cosign verify-blob`, `slsa-verifier verify-artifact`, and look up Rekor entries.

**Rationale**: trond will hold SR private keys and SSH into production servers. Operators
who run validators worth millions of dollars rightly refuse to trust any tool that cannot
be independently verified. Verifiable security is a prerequisite for adoption, not an
optional extra.

### VIII. Progressive Trust and Release

Trust is earned in stages. trond MUST follow a phased release ladder and MUST NOT advertise
production readiness ahead of supporting evidence:

- **alpha (0.x)**: Targets developer machines and local testing only. Open source,
  active community, basic CI.
- **beta (0.9.x)**: Adds Sigstore signing, full CI security scanning, and is suitable for
  testnet (Nile) and non-production environments.
- **1.0.0 (stable)**: Requires a completed third-party security audit and SLSA Level 3
  provenance. Suitable for fullnode production.
- **1.x**: Suitable for SR/witness production after sustained audit cadence, an active
  bounty program, and explicit endorsement from the TRON ecosystem.

The README and release notes MUST clearly indicate the current trust tier. Marketing
language that overstates readiness is forbidden.

**Rationale**: SR operators will continue using manual SSH workflows rather than adopt an
unproven tool. A transparent, evidence-based maturation curve is the only way to earn
their trust over time.

## Boundaries (Out of Scope)

trond MUST NOT do the following. These boundaries exist to keep the project focused and
prevent scope creep into adjacent tools' territory:

- **Provisioning cloud infrastructure** (EC2, GCE, Azure VM, Aliyun ECS, etc.) — that is
  Terraform's, Pulumi's, or the cloud CLI's responsibility.
- **VPC, security group, firewall, IAM, or DNS management** — IaC scope.
- **Operating system installation, base package management, or kernel tuning** — handled by
  cloud-init, Ansible, or distribution images. trond MAY ship a `bootstrap` command that
  installs Docker / JDK on a target via documented scripts, but it does not own OS state.
- **Rewriting tron-docker's Dockerfile or compose templates** — trond consumes them.
- **Embedding any cloud provider SDK** — all clouds reached via SSH or Docker context.
- **Replacing systemd, supervisord, or Kubernetes controllers** — trond emits configuration
  for them.
- **Building a parallel monitoring backend** — trond integrates with Prometheus / Grafana /
  Loki via existing tron-docker compose files.
- **Replacing java-tron's `start.sh`** — `start.sh` continues to live upstream.
- **Providing a Web UI in V1** — postponed indefinitely.

## Security & Supply Chain Requirements

(See Principle VII for the complete normative requirements. This section consolidates
operational expectations.)

- Every release MUST publish: signed binaries, SLSA provenance, SHA256, audit log of
  release-pipeline runs.
- Every PR MUST pass CodeQL, gosec, govulncheck, staticcheck, and OSSF Scorecard checks.
- Every change touching key handling, SSH execution, or audit logging MUST be reviewed by
  at least two CODEOWNERS.
- Vulnerability reports MUST be acknowledged within 24 hours, triaged within 7 days, and
  fixed within 90 days for non-critical issues; critical issues follow an accelerated
  schedule documented in `SECURITY.md`.
- The release pipeline itself MUST run on GitHub-hosted runners (publicly auditable) and
  MUST NOT be triggerable by anyone outside the maintainer team.

## Development Workflow & Release Discipline

- **Branching**: All work happens on feature branches; `main` is protected and accepts only
  PRs that have passed CI and at least one review.
- **Spec-driven**: Material new functionality MUST be introduced through a `/speckit-specify`
  flow followed by `/speckit-plan`, `/speckit-tasks`, `/speckit-implement`. Drive-by
  implementation without a spec is discouraged for anything beyond trivial fixes.
- **Testing**: Every command MUST have unit tests for its core logic and an end-to-end
  smoke test in CI (build trond, deploy a node into a throwaway container, query status,
  destroy). New runtimes and targets MUST land with their own e2e coverage.
- **Versioning**: Strict semantic versioning. Breaking changes (CLI flags removed, intent
  schema field removed, exit codes redefined) require a major-version bump.
- **Cross-repository coordination**: Changes that affect tron-docker or java-tron MUST be
  proposed via an RFC and discussed in the affected repository's issue tracker before
  implementation begins.
- **Migration discipline**: Removal of deprecated CLI subcommands or intent fields MUST
  provide at least one minor version of overlap with deprecation warnings before removal.

## Governance

This Constitution supersedes any informal practice in this repository. All pull requests
and code reviews MUST verify compliance with the principles above. Reviewers MUST request
revisions when a change conflicts with a principle, regardless of the change's other merits.

**Amendments**: Changes to this Constitution MUST be made by pull request that updates
`.specify/memory/constitution.md` and runs the `/speckit-constitution` flow to propagate
implications to dependent templates. Each amendment PR MUST include:

1. The updated constitution text.
2. A Sync Impact Report at the top of the file describing version delta, modified
   principles, added/removed sections, and templates requiring follow-up.
3. A justified semantic-version bump:
   - **MAJOR**: Backward-incompatible removal or redefinition of a principle or governance
     rule.
   - **MINOR**: Addition of a new principle, section, or materially expanded guidance.
   - **PATCH**: Clarifications, typo fixes, non-semantic refinements.
4. Approval from at least two maintainers listed in CODEOWNERS for governance-affecting
   changes; one maintainer suffices for PATCH-level edits.

**Compliance review**: At each minor or major release, maintainers MUST audit the codebase
against this Constitution and document any deviations or pending follow-ups.

**Runtime guidance**: Day-to-day implementation guidance for AI coding agents lives in the
top-level `CLAUDE.md`. The Constitution defines the "what" and "why"; `CLAUDE.md` defines
the "how" for tool use.

**Version**: 1.0.0 | **Ratified**: 2026-04-08 | **Last Amended**: 2026-04-08

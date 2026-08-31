# CLI Contract: trond

**Feature**: 001-trond-cli-platform
**Date**: 2026-04-08

## Global Flags

Every command accepts:

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| --output | -o | enum | text | Output format: text, json. Any other value is refused with exit 2 |
| --log-format | | enum | text | Log format: text, json. Any other value is refused with exit 2 |
| --quiet | -q | bool | false | Suppress non-essential output |
| --verbose | -v | bool | false | Increase log verbosity |
| --no-color | | bool | false | Disable ANSI colors |
| --state-dir | | string | ~/.trond | Directory for state.json, audit.log, deployments (env: TROND_STATE_DIR) |
| --require-private | | bool | false | Refuse to mutate any non-private node; one-way (env: TROND_REQUIRE_PRIVATE) |

## Exit Codes

| Code | Name | Description |
|------|------|-------------|
| 0 | SUCCESS | Operation completed successfully or no changes needed |
| 1 | GENERAL_ERROR | Unclassified error |
| 2 | VALIDATION_ERROR | Intent file or config validation failed |
| 3 | TARGET_UNREACHABLE | SSH connection failed or Docker not available |
| 4 | PREFLIGHT_FAILURE | Target does not meet requirements |
| _(5 unassigned)_ | — | Reserved. `network destroy` reports a partial result as exit 1 with `error_code: "PARTIAL_SUCCESS"` plus a `failed` array; the distinction is in the JSON payload, not the exit status |
| 10 | HUMAN_REQUIRED | Destructive change in non-interactive mode without --auto-approve |

Exit codes are stable across minor versions. New codes may be added in minor releases;
existing codes will not be reassigned without a major version bump.

## Error Output Schema

All errors emitted to stderr as JSON (when `--output json`):

```json
{
  "code": "string (UPPER_SNAKE_CASE error code)",
  "message": "string (human-readable description)",
  "suggestions": ["string (actionable fix suggestion)", "..."]
}
```

## Implementation Contracts

- `internal/apply/hash.go:14-74` is the sole intent-hash source: `EffectiveIntentHash` uses the `intent-hash-v2\x00` domain separator and `v2:` prefix; `LegacyIntentHashMatches` checks raw/canonical forms plus recorded monitoring equivalence; `RestoreAutoPorts` restores seven non-zero persisted ports. CLI apply/plan and MCP plan/apply all call these functions; duplicate implementations are forbidden.
- `internal/state/store.go:64-113` saves through a `0600` temp file, file sync, atomic rename, and directory sync. Save failures propagate as `STATE_ERROR`; the state lock protects read-modify-write from lost updates.
- `internal/runtime/runtime.go:97-107` defines the artifact transaction (`PrepareArtifact`, `Activate`, `Start`, `Rollback`, `Cleanup`). Version state is committed only after activation and startup succeed; jar recovery maintains disk == pin == recorded version.
- `internal/target/target.go:26-107` owns pooled HTTP transports and timeout-enforced dialing. `StreamExec` supports cancellation, explicit close, and natural EOF with merged output and propagated exit errors.
- `cmd/resolve.go:127-146` separates exclusive write resolution (30-second `LOCK_TIMEOUT`) from read-only access. Write commands include apply, start, stop, restart, remove, heal, upgrade, rollback, plus network mutators.
- `internal/render/hocon.go:145-172` provides parse-first `RedactWitnessLines`; plan/config diffs, verify-config, MCP resources, and MCP drift output are redacted, while internal drift comparison uses original bytes.

## Command Reference

### trond apply

**Aliases**: `deploy`

```
trond apply --intent <path> [--auto-approve] [--output json]
```

| Flag | Required | Description |
|------|----------|-------------|
| --intent | yes | Path to intent.yaml |
| --auto-approve | no | Skip confirmation for changes (CI mode) |

**Output (json)**:
```json
{
  "name": "string",
  "status": "running|stopped|error",
  "changes": [{"type": "string", "description": "string"}],
  "endpoints": {"http": "string", "grpc": "string"},
  "runtime": "docker|jar",
  "version": "string"
}
```

### trond plan

```
trond plan --intent <path> [--output json]
```

**Output (json)**:
```json
{
  "name": "string",
  "current_state": "string (summary)",
  "desired_state": "string (summary)",
  "changes": [
    {
      "type": "string",
      "field": "string",
      "from": "any",
      "to": "any",
      "restart_required": "bool"
    }
  ],
  "destructive": "bool",
  "estimated_downtime_seconds": "int"
}
```

### trond status [node]

```
trond status [<node-name>] [--output json]
```

Without node name: lists all nodes (same as `trond list`).
With node name: detailed status.

**Output (json, single node)**:
```json
{
  "name": "string",
  "status": "running|stopped|error|unknown",
  "network": "mainnet|nile|private",
  "runtime": "docker|jar",
  "version": "string",
  "block_height": "int",
  "sync_progress_percent": "float",
  "peer_count": "int",
  "is_synced": "bool",
  "uptime": "string",
  "api_endpoints": {
    "http": "string",
    "grpc": "string",
    "jsonrpc": "string|null"
  }
}
```

### trond diagnose

```
trond diagnose <node-name> [--output json]
```

**Output (json)**:
```json
{
  "name": "string",
  "overall": "healthy|warning|critical",
  "checks": [
    {
      "name": "string (e.g., sync_progress)",
      "status": "pass|warning|fail",
      "message": "string",
      "suggestions": ["string"]
    }
  ]
}
```

### trond config validate

```
trond config validate <intent-path> [--output json]
```

Exit 0 if valid, exit 2 if invalid.

### trond config render

```
trond config render <intent-path> [--output-dir <dir>]
```

Writes rendered HOCON + compose/systemd to output-dir (default: stdout).

### trond verify

```
trond verify --intent <path> [--timeout <duration>] [--output json]
```

Post-deployment health gate. Polls node status until healthy or timeout.

### trond preflight

```
trond preflight --intent <path> [--output json]
```

Checks target readiness without deploying.

### trond knowledge

```
trond knowledge <topic> [--output json]
```

Topics: node-types, troubleshooting, best-practices, cloud-deployment, config-reference.

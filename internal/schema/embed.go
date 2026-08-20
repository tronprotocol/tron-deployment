// Package schema exposes the JSON Schema files that document trond's
// JSON output shapes, plus a cobra-tree walker that produces a
// machine-readable manifest of the whole CLI surface.
//
// Agents call `trond schema -o json` once at session startup, parse
// the manifest, and from then on know every command, every flag, every
// expected output field. They never need to read --help text.
package schema

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// SchemaVersion is the trond CLI contract version. Semver semantics:
//
//   - PATCH: a single existing schema gains an additive optional field
//     (clients that ignore unknown fields are unaffected).
//   - MINOR: an entirely new schema is added (a new command becomes
//     manifest-discoverable; existing schemas unchanged).
//   - MAJOR: an existing field is renamed, removed, or its meaning
//     shifts. Agents pinned to the prior major may break.
//
// Agents should pin to MAJOR for compat detection and re-read AGENTS.md
// when MAJOR bumps. The version bump rationale is also captured in
// CHANGELOG entries.
//
// History:
//
//	1.0.0 — initial 21 schemas (apply, plan, status, list, inspect,
//	        diagnose, health, verify, preflight, doctor, version,
//	        events, config-validate, config-render, network-create,
//	        network-status, snapshot-sources, snapshot-list,
//	        snapshot-download, snapshot-jobs, error envelope).
//	1.1.0 — add recipe-list / recipe-show / recipe-run schemas (no
//	        changes to existing schemas).
//	1.2.0 — add verify-config + auto-heal schemas (new `trond
//	        verify-config` and `trond auto-heal` commands).
//	1.3.0 — add build schema (new `trond build` command + intent
//	        `build:` block; state node entry gains optional
//	        `build_cache_key`).
//	1.4.0 — add build-list / build-inspect / build-prune schemas
//	        (new `trond build list/inspect/prune` cache mgmt
//	        subcommands; existing schemas unchanged).
//	1.5.0 — add shadow-fork-mutate schema (new `trond shadow-fork
//	        mutate` command — Go port of java DbFork; existing
//	        schemas unchanged).
//	1.6.0 — add monitoring schema (new `trond apply --monitor` flag
//	        and intent `monitoring:` block; new `network-create`
//	        `--monitor` flag; apply.output gains `monitoring_error`
//	        and `monitoring` fields).
//	1.7.0 — add `patches` optional field to build entry schemas
//	        (build-list, build-inspect, build-prune) and to the
//	        Manifest. Additive — old clients ignoring unknown
//	        fields are unaffected. Drives FR-026 (declarative
//	        source patches via build.patches).
//	1.8.0 — apply + status (+ list/inspect) output gain `network` and
//	        `is_private` (the C1 private-net safety fact); new
//	        `apply` / `network create --require-private` gate. Additive
//	        optional fields.
//	1.9.0 — status output gains `healthy`, `container_id`, and a
//	        runtime-discriminated `logs` locator; inspect output gains
//	        `logs` (and now populates the already-declared `container_id`).
//	        Completes the ai-ops A1 ask (machine-observable rig state:
//	        container_id + node-log paths) so external read-only tools
//	        (tron-toolkit, mcp-logs) can attach with zero new code.
//	        Additive optional fields.
//	1.10.0 — add snapshot-clone schema (new `trond snapshot clone <src>
//	        <dst>` command — copy-on-write clone of a chain-DB dir into a
//	        fresh path for warm-pool fixtures, ai-ops B3). Existing schemas
//	        unchanged.
//	1.11.0 — `--require-private` becomes a PERSISTENT root flag (+ env
//	        TROND_REQUIRE_PRIVATE) enforced on every per-node mutator
//	        (start/stop/restart/remove/rollback/upgrade) and the MCP apply
//	        tool, not just apply/network-create. Inherited persistent flags
//	        are collected into the `trond schema` manifest, so the manifest
//	        surface changes for every command (additive). The C1 safety
//	        boundary now covers all single-node mutations.
//	1.12.0 — status + inspect output gain `build_cache_key` and a clean
//	        `build_revision` (short git SHA); list output documents
//	        `build_cache_key`. Lets an agent learn which java-tron commit a
//	        node runs without assuming (ai-ops B1: echo the resolved SHA).
//	        Additive optional fields, source-built nodes only.
//	1.12.1 — status output gains `genesis_block_id` (the block-0 TRON block
//	        id, the chain's identity fingerprint, from a live probe). A
//	        single existing schema gaining one additive optional field is a
//	        PATCH per the rule above (the earlier additive bumps that took a
//	        MINOR were over-versioned). `chain_id` itself is deferred — it's
//	        flag-dependent in java-tron (TVM CHAINID = full block id unless
//	        certain VM flags are on); see TODOS.md.
//	1.12.2 — inspect output gains `monitoring` (enabled +
//	        prometheus_port + grafana_port) so agents can discover the
//	        deployed Prometheus/Grafana stack. ManagedNode state gains
//	        `metrics_port` so `network add` rebuilds scrape targets with
//	        the node's real metrics port (instead of hardcoded 9527).
//	        Additive optional fields throughout — PATCH.
//	1.12.3 — config-render output gains `redacted` (top level and per
//	        node). A witness node's private key is no longer inlined
//	        into the rendered `hocon` a preview surface returns; it is
//	        replaced by a `<REDACTED:ENV_NAME>` placeholder and the flag
//	        tells the caller the artifact is a preview java-tron would
//	        reject. Additive optional fields — PATCH.
//	1.12.4 — snapshot-download output gains `plaintext_transport` (both on
//	        the dry-run `preflight` object and the foreground completion),
//	        plus `sha256`, `expected_sha256` and `sha256_verified`. The six
//	        mainnet mirrors publish no HTTPS endpoint, so their transfers —
//	        and the .md5sum sidecar that rides the same connection — are
//	        unauthenticated; an agent can now see that fact instead of
//	        inferring it, and pin an out-of-band digest via `--sha256` /
//	        the MCP `sha256` arg. One existing schema, additive optional
//	        fields only — PATCH.
//	1.12.5 — snapshot-download output gains `verification_skipped`. A
//	        missing or unfetchable `.md5sum` sidecar is now a hard error
//	        (`VERIFICATION_UNAVAILABLE`) rather than a silent skip, so
//	        `md5_verified: false` on a successful download means one
//	        thing only: the operator passed `--no-verify` / `no_verify`.
//	        One existing schema, one additive optional field — PATCH.
const SchemaVersion = "1.16.0"

// JSONSchemaURLBase is the canonical URL prefix for individual output
// schema files. Embedded $id values inside each schema mirror this so
// online and offline validation both work.
const JSONSchemaURLBase = "https://github.com/tronprotocol/tron-deployment/blob/master/schemas/output/"

//go:embed files/*.json
var schemaFS embed.FS

// rawSchemas reads every embedded *.json once at startup, parses each
// into a generic map for round-tripping, and indexes them by short
// name (the basename minus ".schema.json"). Failure is fatal — an
// invalid embedded JSON is a build defect, not a runtime concern.
var rawSchemas = func() map[string]map[string]any {
	out := map[string]map[string]any{}
	entries, err := fs.ReadDir(schemaFS, "files")
	if err != nil {
		panic("schema: cannot read embedded files dir: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		data, err := fs.ReadFile(schemaFS, "files/"+e.Name())
		if err != nil {
			panic("schema: cannot read " + e.Name() + ": " + err.Error())
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			panic("schema: cannot parse " + e.Name() + ": " + err.Error())
		}
		key := strings.TrimSuffix(e.Name(), ".schema.json")
		out[key] = doc
	}
	return out
}()

// Names returns the short names of every embedded schema, sorted.
func Names() []string {
	names := make([]string, 0, len(rawSchemas))
	for k := range rawSchemas {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Get returns the parsed schema by short name (e.g. "apply",
// "snapshot-download"), or false if no schema is registered for that
// command. The returned map is safe to mutate by the caller — it's a
// fresh copy for each request.
func Get(name string) (map[string]any, bool) {
	doc, ok := rawSchemas[name]
	if !ok {
		return nil, false
	}
	return cloneMap(doc), true
}

// URLFor returns the canonical $schema URL for a command's output. It
// matches the `$id` embedded inside the schema file itself.
func URLFor(name string) string {
	return JSONSchemaURLBase + name + ".schema.json"
}

// cloneMap deep-copies a JSON-decoded map so callers can't accidentally
// pollute the embedded schemas at runtime. Only handles the value types
// json.Unmarshal produces (no chan/func/etc.), which is sufficient.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		dup := make([]any, len(x))
		for i, item := range x {
			dup[i] = cloneValue(item)
		}
		return dup
	default:
		return x // strings, numbers, bools, nil are immutable
	}
}

// Errorf is a tiny helper used by the schema cobra command to report
// "no schema for that name" with the same suggestions[] convention as
// the rest of trond's error envelope.
func Errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

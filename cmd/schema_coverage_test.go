package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/schema"
)

// TestSchemaCoverage walks the cobra tree, finds every leaf command
// that supports --output (text / json), and asserts each has a
// matching schemas/output/<name>.schema.json. New commands without
// schemas land here; the alternative is silent: an agent calling the
// command, getting JSON, has no contract to validate against.
//
// `acceptedGaps` is the explicit known-bad list. Adding a name here
// with a one-line reason is the way to acknowledge "this command
// emits JSON but documenting it as schema isn't worth it (yet)".
// Better than skipping the test.
func TestSchemaCoverage(t *testing.T) {
	root := Root()

	// Commands that intentionally don't have a schema today, with
	// the reason. Removing entries here is encouraged — the goal is
	// to drive this list to zero.
	acceptedGaps := map[string]string{
		"trond completion":       "shell completion script — output is shell code, not JSON",
		"trond schema":           "self-describing manifest; documenting its own schema is circular",
		"trond knowledge":        "subcommand parent — knowledge_list / knowledge_get have schemas",
		"trond config":           "subcommand parent — config_validate / config_render / config_diff are leaves",
		"trond network":          "subcommand parent — network create / status / destroy are leaves",
		"trond snapshot":         "subcommand parent — sources / list / jobs / download / prune are leaves",
		"trond network upgrade":  "rolling upgrade; per-node payload reuses upgrade.schema.json (no separate file yet)",
		"trond network destroy":  "destroy emits a result+failures shape that overlaps network-create — to be unified",
		"trond snapshot prune":   "prune output is similar to snapshot-jobs; no separate schema yet",
		"trond snapshot status":  "alias for jobs — covered by snapshot-jobs.schema.json conceptually",
		"trond exec":             "passes through subprocess stdout — no fixed shape",
		"trond logs":             "streams container logs verbatim — no fixed shape",
		"trond files":            "subcommand parent (put / get) — passthrough, no fixed shape",
		"trond files put":        "passthrough subprocess output",
		"trond files get":        "passthrough subprocess output",
		"trond restart":          "shape mirrors stop/start; uses same status-style envelope (no dedicated schema)",
		"trond start":            "stop/start/restart share envelope semantics — see status.schema.json",
		"trond stop":             "see status.schema.json",
		"trond rollback":         "rollback shares apply.schema.json output shape — to be aliased explicitly",
		"trond upgrade":          "upgrade shares apply.schema.json output shape — to be aliased explicitly",
		"trond remove":           "result envelope shape — to be documented",
		"trond bootstrap":        "result envelope shape — to be documented",
		"trond chaos":            "subcommand parent",
		"trond chaos disconnect": "result envelope shape — to be documented",
		"trond chaos connect":    "result envelope shape — to be documented",
		"trond chaos partition":  "result envelope shape — to be documented",
		"trond chaos heal":       "result envelope shape — to be documented",
		"trond wait":             "result envelope shape — to be documented",
		"trond recipe":           "subcommand parent — list / show / run are leaves",
		"trond recipe run":       "uses recipe-run.schema.json (registered under recipe_run not 'run')",
		"trond config docs":      "doc-generation utility, not a runtime command",
		"trond connect":          "chaos primitive — result envelope only",
		"trond disconnect":       "chaos primitive — result envelope only",
		"trond heal":             "chaos primitive — result envelope only",
		"trond partition":        "chaos primitive — result envelope only",
		"trond mcp":              "MCP server (stdio) — emits JSON-RPC, not -o json",
		"trond network add":      "wraps apply with network context — same shape as apply.schema.json",
		"trond snapshot logs":    "streams a single job's log file verbatim, no fixed shape",
		"trond snapshot stop":    "halts a detached download — result envelope only",
	}

	// Map cobra's full command name → schema short name. Mirrors the
	// lookup used by `trond schema`. Keep in sync with cmd/schema.go.
	lookup := map[string]string{
		"trond apply":              "apply",
		"trond auto-heal":          "auto-heal",
		"trond build":              "build",
		"trond build list":         "build-list",
		"trond build inspect":      "build-inspect",
		"trond build prune":        "build-prune",
		"trond config validate":    "config-validate",
		"trond config render":      "config-render",
		"trond config diff":        "config-diff",
		"trond diagnose":           "diagnose",
		"trond doctor":             "doctor",
		"trond events":             "events",
		"trond health":             "health",
		"trond inspect":            "inspect",
		"trond list":               "list",
		"trond network create":     "network-create",
		"trond network status":     "network-status",
		"trond plan":               "plan",
		"trond preflight":          "preflight",
		"trond recipe list":        "recipe-list",
		"trond recipe show":        "recipe-show",
		"trond shadow-fork mutate": "shadow-fork-mutate",
		"trond snapshot download":  "snapshot-download",
		"trond snapshot clone":     "snapshot-clone",
		"trond snapshot jobs":      "snapshot-jobs",
		"trond snapshot list":      "snapshot-list",
		"trond recipe validate":    "recipe-validate",
		"trond snapshot sources":   "snapshot-sources",
		"trond status":             "status",
		"trond verify":             "verify",
		"trond verify-config":      "verify-config",
		"trond version":            "version",
	}

	embeddedSchemas := map[string]bool{}
	for _, n := range schema.Names() {
		embeddedSchemas[n] = true
	}

	var missing []string
	walk(root, func(c *cobra.Command) {
		full := c.CommandPath()
		if !commandHasJSONOutput(c) {
			return
		}
		if _, ok := acceptedGaps[full]; ok {
			return
		}
		schemaName, declared := lookup[full]
		if !declared {
			missing = append(missing, full+" (no schema lookup; declare in cmd/schema.go)")
			return
		}
		if !embeddedSchemas[schemaName] {
			missing = append(missing, full+
				" (lookup → "+schemaName+
				"; not in internal/schema/files/, run `make sync-schemas`)")
		}
	})

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("commands with --output but no schema (or missing from acceptedGaps):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// commandHasJSONOutput returns true when the command (or an ancestor)
// declares a persistent --output flag — every trond command inherits
// it from the root, so this is effectively "is this a leaf
// invocable command".
func commandHasJSONOutput(c *cobra.Command) bool {
	if !c.Runnable() {
		return false
	}
	// Hidden commands don't show up in --help and aren't part of
	// the public surface — exclude them.
	if c.Hidden {
		return false
	}
	if c.Name() == "help" || c.Name() == "completion" {
		return false
	}
	// Every trond command inherits --output from rootCmd's persistent
	// flags. We already confirmed the command is runnable, so it has
	// access to --output.
	return true
}

// walk applies fn depth-first to every cobra command in the tree.
func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, child := range c.Commands() {
		walk(child, fn)
	}
}

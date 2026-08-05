package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `trond exec`, `trond wait --exec` and `trond files put` all shipped able
// to act on a mainnet node while --require-private was set. Each was an
// individual oversight, but they share one shape: the file resolves a node
// context and then does something to that node, and nobody noticed the
// guard was missing because there was no place that would notice.
//
// TestNodeTouchingCommandsCallTheGuard is that place. It derives the set
// of node-touching commands from the source rather than from a hand-kept
// list, so a new command cannot be added to the tree and quietly skip the
// gate: resolving a node makes you either call the guard or write down
// here, with a reason, why you don't need to.
//
// The behavioural half — that the guard actually refuses — lives in
// TestMutators_RefuseMainnetUnderRequirePrivate and the exec/wait/files
// tests. This test only pins that nothing is *unconsidered*.
func TestNodeTouchingCommandsCallTheGuard(t *testing.T) {
	// Files that resolve a node context but legitimately need no gate.
	// Every entry is a read: the gate exists to stop mutation of a
	// non-private rig, and refusing reads would make it useless for the
	// thing it is for (waiting on, inspecting and debugging real nodes).
	//
	// Adding a name here is a decision, not a formality. If the command
	// can change the node or run code against it, it belongs on the other
	// side.
	readOnly := map[string]string{
		"diagnose.go": "read-only diagnostics: inspects state, container status and logs, " +
			"writes nothing to the node",
		"health.go": "read-only probe: curls the node's own HTTP endpoint",
		"logs.go":   "streams the node's logs; no writes, no caller-supplied execution",
		"verify_config.go": "reads the deployed config back off the node (cat) to diff it " +
			"against the intent; no writes",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd dir: %v", err)
	}

	var unguarded []string
	seenReadOnly := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(src)
		if !strings.Contains(body, "resolveNodeContext(") {
			continue
		}
		if strings.Contains(body, "requirePrivateForNode") {
			// Guarded. If it is ALSO on the read-only list that is a
			// contradiction worth surfacing.
			if _, ok := readOnly[name]; ok {
				t.Errorf("%s calls requirePrivateForNode but is listed as read-only; "+
					"remove it from the readOnly map", name)
			}
			continue
		}
		if reason, ok := readOnly[name]; ok {
			seenReadOnly[name] = true
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is listed as read-only with an empty reason", name)
			}
			continue
		}
		unguarded = append(unguarded, name)
	}

	if len(unguarded) > 0 {
		t.Errorf("these files resolve a node but never call requirePrivateForNode: %v\n"+
			"Either add the guard (see cmd/stop.go for the canonical shape), or add the "+
			"file to the readOnly map above with the reason it cannot mutate the node.\n"+
			"This is the check that `trond exec`, `trond wait --exec` and `trond files put` "+
			"each got past.", unguarded)
	}

	// A stale allowlist is its own bug: it means someone added the guard
	// (or deleted the command) and left a note claiming otherwise.
	for name := range readOnly {
		if !seenReadOnly[name] {
			t.Errorf("readOnly lists %q, but that file no longer resolves a node "+
				"without a guard — drop the entry", name)
		}
	}
}

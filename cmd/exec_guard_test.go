package cmd

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// `trond exec` runs a caller-supplied program against a node — on a jar
// node, directly on the target host. It shipped without the
// --require-private gate and without an audit entry, while its nine
// sibling node verbs had both. These tests pin both halves.
//
// The gate itself is covered for every verb by
// TestMutators_RefuseMainnetUnderRequirePrivate; what is here is the
// exec-specific behaviour that table cannot express.

// seedLocalJarNode writes one node the local target can actually exec
// against, with the gate OFF. The node is always named "n0" — these
// tests never need two, and the guard keys off the recorded network, not
// the name.
func seedLocalJarNode(t *testing.T, network string) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{
		Version: 1,
		Nodes: []state.ManagedNode{{
			Name: "n0", Status: "running", Runtime: "jar", Network: network,
			Target:      state.NodeTarget{Type: "local"},
			LastApplied: time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// auditCommands returns the `command` field of every audit line written.
func auditCommands(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(paths.AuditLog())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()

	var got []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(sc.Bytes(), &entry); err != nil {
			t.Fatalf("audit line is not JSON: %v\nraw: %s", err, sc.Bytes())
		}
		if c, _ := entry["command"].(string); c != "" {
			got = append(got, c)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan audit log: %v", err)
	}
	return got
}

// TestExec_WritesAuditEntry pins that an exec leaves a trace. Without it
// the one verb that runs arbitrary code is also the only one that runs
// unrecorded — there is no way to answer "what was run against this
// node" after the fact.
func TestExec_WritesAuditEntry(t *testing.T) {
	tests := []struct {
		name       string
		argv       []string
		wantResult string
		wantErr    bool
	}{
		{"success", []string{"n0", "/bin/echo", "audit-probe"}, "success", false},
		// The failure path must be audited too: an exec that failed still
		// ran, and is the more interesting line in an incident review.
		{"failure", []string{"n0", "/bin/sh", "-c", "exit 3"}, "error", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedLocalJarNode(t, "private")

			err := runExec(newCmd(), tc.argv)
			if tc.wantErr && err == nil {
				t.Fatal("want an error from the failing command, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("runExec: %v", err)
			}

			if got := auditCommands(t); len(got) != 1 || got[0] != "exec" {
				t.Fatalf("audit commands = %v, want exactly [exec]", got)
			}

			// And the recorded result must match what happened.
			f, err := os.ReadFile(paths.AuditLog())
			if err != nil {
				t.Fatalf("read audit log: %v", err)
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(f))), &entry); err != nil {
				t.Fatalf("audit line is not JSON: %v", err)
			}
			if entry["result"] != tc.wantResult {
				t.Errorf("audit result = %v, want %q", entry["result"], tc.wantResult)
			}
			if entry["node"] != "n0" {
				t.Errorf("audit node = %v, want n0", entry["node"])
			}
		})
	}
}

// TestExec_GateOutranksUsageError pins the precedence: with the gate on
// and no command supplied, exec reports PRIVATE_NETWORK_REQUIRED rather
// than a usage error. Same principle as remove's gate-before-confirm —
// an agent should learn "this node is not private" before it learns it
// typed the command wrong, because only one of those facts is a safety
// fact.
func TestExec_GateOutranksUsageError(t *testing.T) {
	seedMainnetNode(t, "n0")
	wantPrivateRequired(t, runExec(newCmd(), []string{"n0"}))
}

// TestExec_UsageErrorWithoutGate pins the other side: with the gate off,
// a missing command is still a usage error and not a silent success.
func TestExec_UsageErrorWithoutGate(t *testing.T) {
	seedLocalJarNode(t, "private")
	err := runExec(newCmd(), []string{"n0"})
	if err == nil {
		t.Fatal("want VALIDATION_ERROR for a missing command, got nil")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error = %v, want a usage error", err)
	}
	if got := auditCommands(t); len(got) != 0 {
		t.Errorf("audit written for an invocation that never ran anything: %v", got)
	}
}

// TestExec_GateAppliesToDockerRuntime pins that the gate is not
// jar-specific. A docker node's exec runs `docker exec <node> ...` inside
// the container, which is still arbitrary code against a mainnet rig.
func TestExec_GateAppliesToDockerRuntime(t *testing.T) {
	// seedMainnetNode seeds Runtime: "docker" and turns the gate on.
	seedMainnetNode(t, "n0")
	wantPrivateRequired(t, runExec(newCmd(), []string{"n0", "/bin/echo", "hi"}))
}

// TestExec_GateOffStillRuns is the no-regression case: exec on a
// non-private node is unchanged when the gate is not requested. The
// guard is opt-in and must stay that way.
func TestExec_GateOffStillRuns(t *testing.T) {
	seedLocalJarNode(t, "mainnet")
	if guard.Requested() {
		t.Fatal("gate unexpectedly on; this test asserts default behaviour")
	}
	if err := runExec(newCmd(), []string{"n0", "/bin/echo", "unchanged"}); err != nil {
		t.Fatalf("exec on a mainnet node without the gate must still work: %v", err)
	}
}

package cmd

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// `trond auto-heal` is a mutator: on a fail check it calls Runtime.Start and
// rewrites the node's recorded status. These tests pin the two halves of the
// --require-private contract for it:
//
//	acting path (no --dry-run) → PRIVATE_NETWORK_REQUIRED on a non-private node
//	--dry-run (proposal only)  → still allowed, preview returned
//
// The refusal tests are hermetic because the gate runs on state ONLY, before
// resolveNodeContext — no docker, no SSH.

// seedHealNode writes one node to an isolated state dir and turns the gate on
// for the test (both restored after). Mirrors seedMainnetNode but lets the
// caller pin status/ports, which the dry-run preview test needs.
func seedHealNode(t *testing.T, node state.ManagedNode) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	node.LastApplied = time.Now().UTC()
	if err := store.Save(&state.DeploymentState{
		Version: 1,
		Nodes:   []state.ManagedNode{node},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	old := guard.FlagValue
	guard.FlagValue = true
	t.Cleanup(func() { guard.FlagValue = old })
}

// closedPort returns a TCP port on 127.0.0.1 with nothing listening, so the
// port_listening checker deterministically fails (which is what makes the
// dry-run preview non-empty).
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}

// TestAutoHeal_RequirePrivate_RefusesMainnet is the core regression for the
// bypass: with the gate on, auto-heal against a mainnet node must refuse with
// the same PRIVATE_NETWORK_REQUIRED / exit 2 envelope `trond start` returns —
// before it can start the node or rewrite its state.
func TestAutoHeal_RequirePrivate_RefusesMainnet(t *testing.T) {
	seedHealNode(t, state.ManagedNode{
		Name: "n0", Status: "stopped", Runtime: "docker", Network: "mainnet",
	})
	oldDry, oldOnly := healDryRun, healOnly
	healDryRun, healOnly = false, nil
	t.Cleanup(func() { healDryRun, healOnly = oldDry, oldOnly })

	wantPrivateRequired(t, runAutoHeal(newCmd(), []string{"n0"}))
}

// TestAutoHeal_RequirePrivate_NotBypassedByOnly proves --only cannot dodge the
// gate: it narrows which checkers run, but the guard sits above the loop.
func TestAutoHeal_RequirePrivate_NotBypassedByOnly(t *testing.T) {
	seedHealNode(t, state.ManagedNode{
		Name: "n0", Status: "stopped", Runtime: "docker", Network: "nile",
	})
	oldDry, oldOnly := healDryRun, healOnly
	healDryRun, healOnly = false, []string{"port_listening"}
	t.Cleanup(func() { healDryRun, healOnly = oldDry, oldOnly })

	wantPrivateRequired(t, runAutoHeal(newCmd(), []string{"n0"}))
}

// TestAutoHeal_RequirePrivate_AllowsDryRun pins the other half of the
// contract: --dry-run never reaches executeHealAction (no Runtime.Start, no
// state write), so the gate must NOT turn it away. An agent under
// TROND_REQUIRE_PRIVATE=1 keeps the remediation preview (healed[].action) it
// cannot get from `trond diagnose`.
func TestAutoHeal_RequirePrivate_AllowsDryRun(t *testing.T) {
	port := closedPort(t)
	seedHealNode(t, state.ManagedNode{
		Name: "n0", Status: "stopped", Runtime: "docker", Network: "mainnet",
		HTTPPort: port,
	})
	oldDry, oldOnly := healDryRun, healOnly
	// --only keeps this hermetic: port_listening dials 127.0.0.1 and never
	// touches the target.
	healDryRun, healOnly = true, []string{"port_listening"}
	t.Cleanup(func() { healDryRun, healOnly = oldDry, oldOnly })

	out := captureStdout(t, func() {
		if err := runAutoHeal(newCmd(), []string{"n0"}); err != nil {
			t.Fatalf("dry-run must not be refused under the gate; got %v", err)
		}
	})

	var got struct {
		DryRun bool `json:"dry_run"`
		Healed []struct {
			Check  string `json:"check"`
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"healed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal auto-heal output %q: %v", out, err)
	}
	if !got.DryRun {
		t.Errorf("dry_run: got false, want true")
	}
	if len(got.Healed) != 1 {
		t.Fatalf("healed: got %d entries (%s), want 1 dry_run preview", len(got.Healed), out)
	}
	if got.Healed[0].Check != "port_listening" || got.Healed[0].Action != "start" ||
		got.Healed[0].Result != "dry_run" {
		t.Errorf("healed[0]: got %+v, want {port_listening start dry_run}", got.Healed[0])
	}
}

// captureStdout runs fn with os.Stdout redirected to a temp file and returns
// what was written. A file (not a pipe) so a large payload can't deadlock.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() {
		os.Stdout = old
		_ = f.Close()
	}()
	fn()
	if err := f.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

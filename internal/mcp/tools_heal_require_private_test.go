package mcp

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// The auto_heal MCP tool is the in-process twin of `trond auto-heal`, and its
// acting path starts the node + rewrites state. These tests pin both halves of
// the --require-private / TROND_REQUIRE_PRIVATE contract on that surface:
// the acting path refuses a non-private node, the dry_run preview does not.

// seedHealNodeMCP writes one node into an isolated state dir and turns the
// gate on for the test (both restored after).
func seedHealNodeMCP(t *testing.T, node state.ManagedNode) {
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

// closedPortMCP returns a 127.0.0.1 TCP port with nothing listening, so the
// port_listening checker fails deterministically and the dry-run preview is
// non-empty.
func closedPortMCP(t *testing.T) int {
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

// TestAutoHealTool_RequirePrivate_RefusesNonPrivate is the MCP-side
// regression: with the gate on, auto_heal against a mainnet node returns the
// PRIVATE_NETWORK_REQUIRED envelope at the early guard — before target
// resolution, and before it could start the node or save state.
func TestAutoHealTool_RequirePrivate_RefusesNonPrivate(t *testing.T) {
	seedHealNodeMCP(t, state.ManagedNode{
		Name: "n0", Status: "stopped", Runtime: "docker", Network: "mainnet",
	})

	res, _, err := autoHealTool(context.Background(), nil, autoHealArgs{Name: "n0"})
	if err != nil {
		t.Fatalf("autoHealTool returned a transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected an error result (gate on, mainnet node), got %+v", res)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "PRIVATE_NETWORK_REQUIRED") {
		t.Errorf("error result should carry PRIVATE_NETWORK_REQUIRED; got:\n%s", text)
	}
}

// TestAutoHealTool_RequirePrivate_AllowsDryRun pins the carve-out: dry_run
// only proposes (it returns before mcpRunHealAction and the state save), so
// the gate must not turn it away.
func TestAutoHealTool_RequirePrivate_AllowsDryRun(t *testing.T) {
	seedHealNodeMCP(t, state.ManagedNode{
		Name: "n0", Status: "stopped", Runtime: "docker", Network: "mainnet",
		HTTPPort: closedPortMCP(t),
	})

	res, _, err := autoHealTool(context.Background(), nil, autoHealArgs{Name: "n0", DryRun: true})
	if err != nil {
		t.Fatalf("autoHealTool returned a transport error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("dry_run must not be refused under the gate; got %s", extractText(t, res))
	}

	body := extractText(t, res)
	var got struct {
		DryRun bool `json:"dry_run"`
		Healed []struct {
			Check  string `json:"check"`
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"healed"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal auto_heal body %q: %v", body, err)
	}
	if !got.DryRun {
		t.Errorf("dry_run: got false, want true")
	}
	found := false
	for _, h := range got.Healed {
		if h.Check == "port_listening" && h.Action == "start" && h.Result == "dry_run" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a port_listening/start/dry_run preview entry; got:\n%s", body)
	}
}

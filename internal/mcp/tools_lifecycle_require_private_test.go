package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// TestApplyTool_RequirePrivate_RefusesNonPrivate is the MCP-side C1 guard
// test: apply with require_private=true against a mainnet intent must refuse
// at the EARLY guard (before target resolution / HUMAN_REQUIRED) with
// PRIVATE_NETWORK_REQUIRED — not TARGET_UNREACHABLE, HUMAN_REQUIRED, or a
// re-wrapped DEPLOY_ERROR. No docker needed: it returns at guard time.
func TestApplyTool_RequirePrivate_RefusesNonPrivate(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	body := "name: guard-mcp\nnetwork: mainnet\n" +
		"target:\n  type: local\n  runtime: docker\n" +
		"nodes:\n  - type: fullnode\n    version: latest\n"
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res, _, err := applyTool(context.Background(), nil, applyArgs{Path: intentPath, RequirePrivate: true})
	if err != nil {
		t.Fatalf("applyTool returned a transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected an error result, got %+v", res)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "PRIVATE_NETWORK_REQUIRED") {
		t.Errorf("error result should carry PRIVATE_NETWORK_REQUIRED; got:\n%s", text)
	}
}

// TestApplyTool_RequirePrivate_RefusesPrivateLabelOverMainnetNode is the
// MCP-side state-side gate: the intent's `network:` is a caller-supplied
// label, so when a node is already deployed under that name the gate must be
// decided by the network recorded in state. Otherwise an agent holding
// require_private could hand in `name: <mainnet node>` / `network: private`
// and re-deploy a production node. Refuses before target resolution, so no
// docker is needed.
func TestApplyTool_RequirePrivate_RefusesPrivateLabelOverMainnetNode(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "tron-prod", Status: "running", Runtime: "docker",
		Network: "mainnet", LastApplied: time.Now().UTC(),
	})

	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	body := "name: tron-prod\nnetwork: private\n" +
		"target:\n  type: local\n  runtime: docker\n" +
		"nodes:\n  - type: fullnode\n    version: latest\n"
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res, _, err := applyTool(context.Background(), nil, applyArgs{Path: intentPath, RequirePrivate: true})
	if err != nil {
		t.Fatalf("applyTool returned a transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected an error result (mainnet node, require_private), got %+v", res)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "PRIVATE_NETWORK_REQUIRED") {
		t.Errorf("error result should carry PRIVATE_NETWORK_REQUIRED; got:\n%s", text)
	}

	// And the node's recorded network must be untouched.
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.GetNode(st, "tron-prod").Network; got != "mainnet" {
		t.Errorf("recorded network = %q; want mainnet", got)
	}
}

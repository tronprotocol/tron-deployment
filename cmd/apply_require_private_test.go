package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// TestRunApply_RequirePrivate_RefusesNonPrivate is the C1 cmd guard test:
// `apply --require-private` against a non-private intent must refuse with
// a PRIVATE_NETWORK_REQUIRED / exit-2 envelope at the early guard, BEFORE
// touching the target (no docker needed — it returns at intent-load time).
//
// We deliberately only exercise the REFUSAL path here. The "private is
// allowed" direction is covered by apply.TestApply_PrivateNetwork_ResultAndState
// and apply.TestApply_RequirePrivate_RefusesInCore (both fakeTarget, no
// real deploy) — driving it through runApply would actually deploy a
// container, which under `make e2e` (docker present, default ports) would
// squat on 8090/50051/18888 and poison the other e2e tests. (That bug is
// exactly what this test used to cause.)
func TestRunApply_RequirePrivate_RefusesNonPrivate(t *testing.T) {
	// Isolate state so we never read/write the real ~/.trond.
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	// Global flag state is package-level; save + restore.
	oldPath, oldReq := applyIntentPath, guard.FlagValue
	t.Cleanup(func() { applyIntentPath, guard.FlagValue = oldPath, oldReq })

	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	body := "name: guard-test\nnetwork: mainnet\n" +
		"target:\n  type: local\n  runtime: docker\n" +
		"nodes:\n  - type: fullnode\n    version: latest\n"
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	applyIntentPath = intentPath
	guard.FlagValue = true

	err := runApply(cmd, nil)
	if err == nil {
		t.Fatal("expected refusal for mainnet + --require-private, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" {
		t.Errorf("error_code = %q; want PRIVATE_NETWORK_REQUIRED", se.Code)
	}
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit_code = %d; want %d", se.ExitCode, output.ExitValidationError)
	}
}

// TestRunApply_RequirePrivate_RefusesPrivateLabelOverMainnetNode is the
// state-side half of the gate: an intent may CLAIM `network: private`, but
// when a node is already deployed under that name the gate is decided by the
// network recorded in state. Without it, `name: <mainnet node>` /
// `network: private` re-rendered and re-deployed a production node under
// TROND_REQUIRE_PRIVATE=1 (and then relabelled it private in state, disarming
// every later gate check). Hermetic: the refusal happens before target
// resolution, so no docker/SSH is involved.
func TestRunApply_RequirePrivate_RefusesPrivateLabelOverMainnetNode(t *testing.T) {
	// Seeds a mainnet node "tron-prod" into an isolated state dir and turns
	// the gate on (both restored on cleanup).
	seedMainnetNode(t, "tron-prod")

	oldPath := applyIntentPath
	t.Cleanup(func() { applyIntentPath = oldPath })

	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	body := "name: tron-prod\nnetwork: private\n" +
		"target:\n  type: local\n  runtime: docker\n" +
		"nodes:\n  - type: fullnode\n    version: latest\n"
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	applyIntentPath = intentPath

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := runApply(cmd, nil)
	if err == nil {
		t.Fatal("expected refusal: private-labelled intent over a mainnet node with the gate on")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" || se.ExitCode != output.ExitValidationError {
		t.Errorf("got code=%q exit=%d; want PRIVATE_NETWORK_REQUIRED/%d",
			se.Code, se.ExitCode, output.ExitValidationError)
	}

	// The refusal must not have rewritten the node's recorded network.
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

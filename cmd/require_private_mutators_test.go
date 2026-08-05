package cmd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// seedMainnetNode writes a single mainnet node to an isolated state dir and
// turns the --require-private gate on for the test (restored after). A
// mutator that runs the state-only guard BEFORE resolveNodeContext refuses
// here without ever touching docker/SSH — so this is hermetic.
func seedMainnetNode(t *testing.T, name string) {
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
			Name: name, Status: "running", Runtime: "docker",
			Network: "mainnet", LastApplied: time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	old := guard.FlagValue
	guard.FlagValue = true
	t.Cleanup(func() { guard.FlagValue = old })
}

func newCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().String("output", "json", "")
	return c
}

func wantPrivateRequired(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected PRIVATE_NETWORK_REQUIRED, got nil (mutator ran on a mainnet node with the gate on)")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" || se.ExitCode != output.ExitValidationError {
		t.Errorf("got code=%q exit=%d; want PRIVATE_NETWORK_REQUIRED/%d", se.Code, se.ExitCode, output.ExitValidationError)
	}
}

// TestMutators_RefuseMainnetUnderRequirePrivate proves every per-node mutator
// runs the gate (and runs it before any docker/SSH work — the test would
// otherwise need a real runtime). A guard accidentally placed AFTER the
// mutation would fail here because the call wouldn't return PRIVATE_NETWORK_REQUIRED.
func TestMutators_RefuseMainnetUnderRequirePrivate(t *testing.T) {
	mutators := map[string]func(*cobra.Command, []string) error{
		"start":     runStart,
		"stop":      runStop,
		"restart":   runRestart,
		"rollback":  runRollback,
		"upgrade":   runUpgrade,
		"remove":    runRemove,
		"auto-heal": runAutoHeal,
		// exec runs a caller-supplied program against the node — on a jar
		// node directly on the target host. Its absence from this table is
		// what let it ship ungated: every sibling was covered, so the table
		// looked exhaustive. Note the args here are just {"n0"}: exec gates
		// before its own "no command supplied" usage check precisely so the
		// safety fact outranks the usage error.
		"exec": runExec,
	}
	for name, run := range mutators {
		t.Run(name, func(t *testing.T) {
			seedMainnetNode(t, "n0")
			if name == "upgrade" {
				old := upgradeVersion
				upgradeVersion = "GreatVoyage-v4.8.0"
				t.Cleanup(func() { upgradeVersion = old })
			}
			if name == "auto-heal" {
				// The table covers the ACTING path. --dry-run only proposes
				// (nothing started, no state written) and stays allowed —
				// see TestAutoHeal_RequirePrivate_AllowsDryRun.
				old := healDryRun
				healDryRun = false
				t.Cleanup(func() { healDryRun = old })
			}
			wantPrivateRequired(t, run(newCmd(), []string{"n0"}))
		})
	}
}

// TestRemove_PrivateGuardPrecedesConfirm proves the C1 precedence: with the
// gate on and NO --confirm, remove returns PRIVATE_NETWORK_REQUIRED (exit 2),
// NOT HUMAN_REQUIRED (exit 10) — the agent learns "not private" before being
// asked to confirm a destroy.
func TestRemove_PrivateGuardPrecedesConfirm(t *testing.T) {
	seedMainnetNode(t, "n0")
	oldConfirm := removeConfirm
	removeConfirm = "" // no confirmation supplied
	t.Cleanup(func() { removeConfirm = oldConfirm })

	wantPrivateRequired(t, runRemove(newCmd(), []string{"n0"}))
}

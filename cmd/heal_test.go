package cmd

import (
	"context"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type captureNetworkChecker struct{ got *string }

func (c captureNetworkChecker) Name() string { return "capture_network" }
func (c captureNetworkChecker) Run(_ context.Context, _ target.Target, opts diagnosis.CheckOpts) diagnosis.CheckResult {
	*c.got = opts.Network
	return diagnosis.CheckResult{Name: c.Name(), Status: diagnosis.StatusPass, Message: "captured"}
}

// TestProposeHealAction pins the (check, current state) → action
// mapping for `trond auto-heal`. Every case here is a contract:
// adding a new mapping requires adding a case here so the table
// stays the source of truth.
func TestProposeHealAction(t *testing.T) {
	cases := []struct {
		name       string
		check      diagnosis.CheckResult
		nodeStatus string
		wantOK     bool
		wantAction string
	}{
		{
			name: "port-listening-fail-stopped-node-can-be-started",
			check: diagnosis.CheckResult{
				Name:   "port_listening",
				Status: diagnosis.StatusFail,
			},
			nodeStatus: "stopped",
			wantOK:     true,
			wantAction: "start",
		},
		{
			name: "port-listening-fail-running-node-no-auto-fix",
			check: diagnosis.CheckResult{
				Name:   "port_listening",
				Status: diagnosis.StatusFail,
			},
			// If state thinks the node is running but ports aren't
			// listening, auto-restart is risky (e.g. the container
			// may be in the middle of a long startup); surface to
			// human instead.
			nodeStatus: "running",
			wantOK:     false,
		},
		{
			name: "sync-progress-fail-no-auto-fix",
			check: diagnosis.CheckResult{
				Name:   "sync_progress",
				Status: diagnosis.StatusFail,
			},
			nodeStatus: "running",
			wantOK:     false,
		},
		{
			name: "peer-count-fail-no-auto-fix",
			check: diagnosis.CheckResult{
				Name:   "peer_count",
				Status: diagnosis.StatusFail,
			},
			nodeStatus: "running",
			wantOK:     false,
		},
		{
			name: "disk-space-fail-no-auto-fix",
			check: diagnosis.CheckResult{
				Name:   "disk_space",
				Status: diagnosis.StatusFail,
			},
			nodeStatus: "running",
			wantOK:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := proposeHealAction(tc.check, tc.nodeStatus)
			if ok != tc.wantOK {
				t.Errorf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && got.Action != tc.wantAction {
				t.Errorf("action: got %q, want %q", got.Action, tc.wantAction)
			}
		})
	}
}

func TestRunDiagnosePassesRecordedNetworkToChecker(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "diag-net", Network: "private", Runtime: "docker", Status: "stopped",
	}}}); err != nil {
		t.Fatal(err)
	}
	var got string
	old := diagnoseCheckers
	diagnoseCheckers = func() []diagnosis.Checker { return []diagnosis.Checker{captureNetworkChecker{got: &got}} }
	t.Cleanup(func() { diagnoseCheckers = old })

	cmd := diagnoseCmd
	cmd.SetArgs([]string{"diag-net"})
	if err := runDiagnose(cmd, []string{"diag-net"}); err != nil {
		t.Fatal(err)
	}
	if got != "private" {
		t.Fatalf("checker network=%q, want private", got)
	}
}

func TestRunAutoHealPassesRecordedNetworkToChecker(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "heal-net", Network: "nile", Runtime: "docker", Status: "stopped",
	}}}); err != nil {
		t.Fatal(err)
	}
	var got string
	old := healCheckers
	healCheckers = func() []diagnosis.Checker { return []diagnosis.Checker{captureNetworkChecker{got: &got}} }
	t.Cleanup(func() { healCheckers = old })

	oldDryRun := healDryRun
	healDryRun = true
	t.Cleanup(func() { healDryRun = oldDryRun })
	cmd := autoHealCmd
	cmd.SetContext(context.Background())
	if err := runAutoHeal(cmd, []string{"heal-net"}); err != nil {
		t.Fatal(err)
	}
	if got != "nile" {
		t.Fatalf("checker network=%q, want nile", got)
	}
}

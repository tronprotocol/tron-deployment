package network

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// `network add` is gated on TWO networks:
//
//	1. the intent's own `network:` label  — keeps a mainnet/nile node out of
//	   a private enclave (guard.Enforce, fires first, reads no state);
//	2. the ENCLAVE named by --network      — keeps a "private"-labelled node
//	   out of a mainnet/nile enclave (guard.EnforceNodes over the networks
//	   recorded in state for the enclave's members, exactly as `network
//	   destroy` / `network upgrade` do).
//
// Check 2 is what these tests were added for: the intent's label is written
// by the caller, while the resource being mutated is the enclave — its shared
// docker network, its nodes' P2P mesh, its Prometheus config.

// setGate forces the --require-private gate on or off for one test. Call it
// AFTER seedNetwork (which turns the gate on); cleanups unwind in reverse.
// Turning it off also neutralises TROND_REQUIRE_PRIVATE, since the gate is a
// one-way floor: a truthy env alone keeps it on.
func setGate(t *testing.T, on bool) {
	t.Helper()
	old := guard.FlagValue
	guard.FlagValue = on
	t.Cleanup(func() { guard.FlagValue = old })
	if !on {
		t.Setenv(guard.EnvVar, "")
	}
}

// addIntentFile writes a single-node intent declaring chain (mainnet | nile |
// private) and points the `network add` flags at it plus the enclave name.
//
// The target is always an ssh host whose identity_file does not exist.
// SSHTarget.Connect fails on that file read before opening any socket, so:
//   - an ALLOWED add stops deterministically at TARGET_UNREACHABLE — the step
//     immediately after both gate checks — proving the gates let it through
//     without deploying a container;
//   - and a REFUSED add can never fall through to a real `docker compose up`
//     if the gate ever regresses, so these tests stay hermetic either way.
func addIntentFile(t *testing.T, enclave, chain string) {
	t.Helper()
	targetBlock := "target:\n  type: ssh\n  runtime: docker\n" +
		"  host: 203.0.113.1\n  user: deploy\n" +
		"  identity_file: " + filepath.Join(t.TempDir(), "no-such-key") + "\n"
	body := "name: added-node\nnetwork: " + chain + "\n" + targetBlock +
		"nodes:\n  - type: fullnode\n    version: latest\n"

	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	oldNet, oldPath := addNetworkName, addIntentPath
	addNetworkName, addIntentPath = enclave, path
	t.Cleanup(func() { addNetworkName, addIntentPath = oldNet, oldPath })
}

func wantStructuredCode(t *testing.T, err error, code string) *output.StructuredError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", code)
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != code {
		t.Fatalf("got code=%q (%v); want %s", se.Code, err, code)
	}
	return se
}

// wantNodeCount asserts the refusal did not record a node.
func wantNodeCount(t *testing.T, want int) {
	t.Helper()
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(st.Nodes); got != want {
		t.Errorf("state holds %d node(s); want %d (a refused add must not record anything)", got, want)
	}
}

// TestNetworkAdd_RefusesPrivateIntentIntoNonPrivateEnclave is the regression:
// with the gate on, an intent that CLAIMS `network: private` must not be able
// to inject a node into an enclave whose recorded members are mainnet. The
// added container would join `trond-<enclave>`, be wired as an active P2P
// peer of the production nodes, and be scraped by the enclave's Prometheus.
// Hermetic: the refusal happens before target resolution, so no docker/SSH.
func TestNetworkAdd_RefusesPrivateIntentIntoNonPrivateEnclave(t *testing.T) {
	seedNetwork(t, "prod", "mainnet", "mainnet")
	addIntentFile(t, "prod", "private")

	se := wantStructuredCode(t, runAdd(newNetCmd(), nil), "PRIVATE_NETWORK_REQUIRED")
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit=%d; want %d (identical to network destroy/upgrade)", se.ExitCode, output.ExitValidationError)
	}
	// The refusal must come from the ENCLAVE check (which names the first
	// offending member), not from the intent-label check — the intent says
	// "private", so only the state-side check can catch this.
	if !strings.Contains(se.Message, "prod-node0") {
		t.Errorf("message should name the offending enclave member; got %q", se.Message)
	}
	wantNodeCount(t, 2)
}

// TestNetworkAdd_RefusesNonPrivateIntentIntoPrivateEnclave pins the other
// half: the intent-label check still refuses a mainnet-labelled node being
// added to a genuinely private enclave. It fires first (before any state is
// consulted), so a mainnet node can never be smuggled into a private rig.
func TestNetworkAdd_RefusesNonPrivateIntentIntoPrivateEnclave(t *testing.T) {
	seedNetwork(t, "rig", "private", "private")
	addIntentFile(t, "rig", "mainnet")

	se := wantStructuredCode(t, runAdd(newNetCmd(), nil), "PRIVATE_NETWORK_REQUIRED")
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit=%d; want %d", se.ExitCode, output.ExitValidationError)
	}
	if !strings.Contains(se.Message, "mainnet") {
		t.Errorf("message should name the intent's non-private network; got %q", se.Message)
	}
	wantNodeCount(t, 2)
}

// TestNetworkAdd_RefusesUnknownEnclaveUnderGate: an enclave with no recorded
// members cannot be proven private, so under the gate `add` fails closed —
// and a private node belonging to a DIFFERENT enclave does not authorise it.
func TestNetworkAdd_RefusesUnknownEnclaveUnderGate(t *testing.T) {
	seedNetwork(t, "other", "private")
	addIntentFile(t, "prod", "private")

	se := wantStructuredCode(t, runAdd(newNetCmd(), nil), "PRIVATE_NETWORK_REQUIRED")
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit=%d; want %d", se.ExitCode, output.ExitValidationError)
	}
	if !strings.Contains(se.Message, `"prod"`) {
		t.Errorf("message should name the unknown enclave; got %q", se.Message)
	}
	wantNodeCount(t, 1)
}

// TestNetworkAdd_AllowsPrivateIntoPrivateEnclave: the case the gate exists to
// permit still works. Both checks pass and the call proceeds to the next step
// (target resolution), which fails only because the test's SSH identity file
// does not exist.
func TestNetworkAdd_AllowsPrivateIntoPrivateEnclave(t *testing.T) {
	seedNetwork(t, "rig", "private", "private")
	addIntentFile(t, "rig", "private")

	wantStructuredCode(t, runAdd(newNetCmd(), nil), "TARGET_UNREACHABLE")
}

// TestNetworkAdd_GateOff_NonPrivateEnclaveStillAllowed: with the gate off,
// adding a mainnet node to a mainnet enclave behaves exactly as before — the
// new enclave check reads no state and refuses nothing.
func TestNetworkAdd_GateOff_NonPrivateEnclaveStillAllowed(t *testing.T) {
	seedNetwork(t, "prod", "mainnet", "mainnet")
	setGate(t, false)
	addIntentFile(t, "prod", "mainnet")

	wantStructuredCode(t, runAdd(newNetCmd(), nil), "TARGET_UNREACHABLE")
}

// TestNetworkAdd_GateOff_UnknownEnclaveStillAllowed: the fail-closed
// empty-enclave rule is scoped to the gate. With the gate off, adding to a
// network with no recorded members proceeds exactly as it did before.
func TestNetworkAdd_GateOff_UnknownEnclaveStillAllowed(t *testing.T) {
	seedNetwork(t, "other", "private")
	setGate(t, false)
	addIntentFile(t, "prod", "private")

	wantStructuredCode(t, runAdd(newNetCmd(), nil), "TARGET_UNREACHABLE")
}

// TestEnforceEnclavePrivate covers member selection directly: which recorded
// nodes count as members of the enclave being joined. The selector is the
// same "<enclave>-node" prefix runAdd uses to pick the next index, to wire
// active_peers and to rebuild Prometheus targets — so the guarded set is
// exactly the set an add touches.
func TestEnforceEnclavePrivate(t *testing.T) {
	node := func(name, network string) state.ManagedNode {
		return state.ManagedNode{Name: name, Network: network}
	}
	tests := []struct {
		name    string
		gate    bool
		enclave string
		nodes   []state.ManagedNode
		wantErr bool
	}{
		{
			name: "gate on, all members private", gate: true, enclave: "rig",
			nodes: []state.ManagedNode{node("rig-node0", "private"), node("rig-node1", "private")},
		},
		{
			name: "gate on, one member mainnet", gate: true, enclave: "rig",
			nodes:   []state.ManagedNode{node("rig-node0", "private"), node("rig-node1", "mainnet")},
			wantErr: true,
		},
		{
			name: "gate on, double-digit member is still a member", gate: true, enclave: "rig",
			nodes:   []state.ManagedNode{node("rig-node0", "private"), node("rig-node10", "nile")},
			wantErr: true,
		},
		{
			name: "gate on, member with no recorded network", gate: true, enclave: "rig",
			nodes:   []state.ManagedNode{node("rig-node0", "")},
			wantErr: true,
		},
		{
			name: "gate on, no recorded members", gate: true, enclave: "rig",
			nodes:   []state.ManagedNode{node("other-node0", "private")},
			wantErr: true,
		},
		{
			// A longer name that merely shares a prefix is a DIFFERENT
			// enclave ("rig-node" vs "rigtest-node"): add never peers with
			// it or scrapes it, so it must not decide this gate either way.
			name: "gate on, similarly-named enclave is not a member", gate: true, enclave: "rig",
			nodes: []state.ManagedNode{node("rig-node0", "private"), node("rigtest-node0", "mainnet")},
		},
		{
			name: "gate off, mainnet members", gate: false, enclave: "rig",
			nodes: []state.ManagedNode{node("rig-node0", "mainnet")},
		},
		{
			name: "gate off, no recorded members", gate: false, enclave: "rig",
			nodes: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setGate(t, tc.gate)
			err := enforceEnclavePrivate(&state.DeploymentState{Version: 1, Nodes: tc.nodes}, tc.enclave)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("enforceEnclavePrivate = %v; want nil", err)
				}
				return
			}
			se := wantStructuredCode(t, err, "PRIVATE_NETWORK_REQUIRED")
			if se.ExitCode != output.ExitValidationError {
				t.Errorf("exit=%d; want %d", se.ExitCode, output.ExitValidationError)
			}
		})
	}
}

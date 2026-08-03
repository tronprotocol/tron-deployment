package apply

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// The --require-private gate has to be decided by the network of the node
// being MUTATED, not by the label in the intent the caller happens to pass.
// These tests pin both halves of that contract in the core:
//
//	gate on  + node recorded non-private → PRIVATE_NETWORK_REQUIRED
//	gate off + non-private node relabelled "private" → NETWORK_MISMATCH
//
// plus the cases that must keep working: private-over-private, ordinary
// non-private updates with the gate off, and the legacy backfill of a node
// with no recorded network.

// deployableIntent returns a jar-runtime intent for the given name/network,
// installing under dir. jar + fakeTarget means Apply runs end-to-end with no
// docker daemon and no writes outside dir.
func deployableIntent(name, network, dir string) *intent.Intent {
	return &intent.Intent{
		Name:    name,
		Network: network,
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051},
			InstallPath: filepath.Join(dir, "install"),
		}},
	}
}

// seedRecordedNode puts one already-deployed node into a fresh store.
func seedRecordedNode(t *testing.T, name, network, intentHash string) (*state.Store, *state.DeploymentState) {
	t.Helper()
	store, st := freshStore(t)
	store.UpsertNode(st, state.ManagedNode{
		Name:        name,
		Network:     network,
		IntentHash:  intentHash,
		ConfigHash:  "cfg",
		Version:     "4.8.1",
		Runtime:     "docker",
		Status:      "running",
		LastApplied: time.Now().UTC(),
	})
	return store, st
}

func wantErrCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil (apply proceeded)", code)
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != code || se.ExitCode != output.ExitValidationError {
		t.Errorf("got code=%q exit=%d; want %s/%d", se.Code, se.ExitCode, code, output.ExitValidationError)
	}
}

// TestApply_RequirePrivate_RefusesPrivateLabelOverRecordedMainnet is the core
// regression: under the gate, an intent that names an already-deployed mainnet
// node but labels itself `network: private` must refuse — the label is
// caller-supplied, the node's real network is the one in state.
func TestApply_RequirePrivate_RefusesPrivateLabelOverRecordedMainnet(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "tron-prod", "mainnet", "deployed-hash")
	existing := *store.GetNode(st, "tron-prod")

	_, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("tron-prod", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "attacker-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: true,
	})
	wantErrCode(t, err, "PRIVATE_NETWORK_REQUIRED")

	if got := st.Nodes[0].Network; got != "mainnet" {
		t.Errorf("recorded network = %q; want mainnet (the gate must never relabel it)", got)
	}
	if got := st.Nodes[0].IntentHash; got != "deployed-hash" {
		t.Errorf("recorded intent hash = %q; want deployed-hash (refused apply must not persist)", got)
	}
}

// TestApply_RequirePrivate_RefusesWhenExistingNotPassed proves the gate does
// not depend on the caller populating Options.Existing: the core resolves the
// node from the state it was handed.
func TestApply_RequirePrivate_RefusesWhenExistingNotPassed(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "tron-prod", "mainnet", "deployed-hash")

	_, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("tron-prod", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "attacker-hash",
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: true,
	})
	wantErrCode(t, err, "PRIVATE_NETWORK_REQUIRED")
}

// TestApply_RequirePrivate_RefusesOnNoChangePath pins the gate ABOVE the
// idempotency short-circuit: with a matching intent hash the no_change path
// would otherwise "backfill" the recorded network from the intent, i.e.
// relabel a mainnet node private while reporting no_change.
func TestApply_RequirePrivate_RefusesOnNoChangePath(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "tron-prod", "mainnet", "same-hash")
	existing := *store.GetNode(st, "tron-prod")

	_, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("tron-prod", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "same-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: true,
	})
	wantErrCode(t, err, "PRIVATE_NETWORK_REQUIRED")

	if got := st.Nodes[0].Network; got != "mainnet" {
		t.Errorf("recorded network = %q; want mainnet", got)
	}
}

// TestApply_RefusesPrivateRelabel_WithGateOff is the defence-in-depth half:
// even with the gate OFF, apply must not silently rewrite a node recorded on a
// non-private network to "private" — that single write is what disarms the
// gate for every later verb (stop/remove --purge/rollback/chaos).
func TestApply_RefusesPrivateRelabel_WithGateOff(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "tron-prod", "mainnet", "deployed-hash")
	existing := *store.GetNode(st, "tron-prod")

	_, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("tron-prod", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "new-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: false,
	})
	wantErrCode(t, err, "NETWORK_MISMATCH")

	if got := st.Nodes[0].Network; got != "mainnet" {
		t.Errorf("recorded network = %q; want mainnet (never downgraded to private)", got)
	}
}

// TestApply_RequirePrivate_AllowsPrivateOverPrivate is the happy path the gate
// exists to permit: re-applying a private intent over a node recorded private
// still deploys and still records private.
func TestApply_RequirePrivate_AllowsPrivateOverPrivate(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "priv-node", "private", "old-hash")
	existing := *store.GetNode(st, "priv-node")

	res, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("priv-node", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "new-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: true,
	})
	if err != nil {
		t.Fatalf("private-over-private apply must succeed under the gate; got %v", err)
	}
	if res.Outcome != "updated" {
		t.Errorf("Outcome = %q; want updated", res.Outcome)
	}
	if st.Nodes[0].Network != "private" {
		t.Errorf("recorded network = %q; want private", st.Nodes[0].Network)
	}
}

// TestApply_GateOff_NonPrivateUpdateStillApplies proves default behaviour is
// unchanged: with the gate off, updating a nile node from a nile intent
// deploys and records exactly as before.
func TestApply_GateOff_NonPrivateUpdateStillApplies(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "nile-node", "nile", "old-hash")
	existing := *store.GetNode(st, "nile-node")

	res, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("nile-node", "nile", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "new-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("gate-off nile update must still apply; got %v", err)
	}
	if res.Outcome != "updated" || res.Network != "nile" {
		t.Errorf("got outcome=%q network=%q; want updated/nile", res.Outcome, res.Network)
	}
	if st.Nodes[0].Network != "nile" {
		t.Errorf("recorded network = %q; want nile", st.Nodes[0].Network)
	}
}

// TestApply_RequirePrivate_RefusesUnrecordedNetwork pins the fail-safe side of
// the legacy case: UNDER the gate, a node whose network was never recorded
// cannot be proven private, so re-applying it refuses exactly like every other
// mutator does (`trond start`, `stop`, …). The escape hatch is the one the
// guard suggests — re-apply once without --require-private / the env, which
// records the network (see TestApply_BackfillsUnrecordedNetwork).
func TestApply_RequirePrivate_RefusesUnrecordedNetwork(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "legacy-node", "", "old-hash")
	existing := *store.GetNode(st, "legacy-node")

	_, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("legacy-node", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "new-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
		RequirePrivate: true,
	})
	wantErrCode(t, err, "PRIVATE_NETWORK_REQUIRED")
}

// TestApply_BackfillsUnrecordedNetwork keeps the legacy path working: a node
// deployed before ManagedNode.Network existed has an EMPTY network, and
// re-applying it (gate off) still records the intent's value. That is a
// backfill of a missing fact, not a downgrade of a known one.
func TestApply_BackfillsUnrecordedNetwork(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := seedRecordedNode(t, "legacy-node", "", "same-hash")
	existing := *store.GetNode(st, "legacy-node")

	res, err := Apply(context.Background(), Options{
		Intent:         deployableIntent("legacy-node", "private", dir),
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "same-hash",
		Existing:       &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("legacy backfill must still apply with the gate off; got %v", err)
	}
	if res.Outcome != "no_change" {
		t.Errorf("Outcome = %q; want no_change", res.Outcome)
	}
	if st.Nodes[0].Network != "private" {
		t.Errorf("recorded network = %q; want private (backfilled)", st.Nodes[0].Network)
	}
}

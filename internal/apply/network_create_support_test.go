package apply

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

func peerIntent(t *testing.T, dir string, mon *intent.Monitoring) *intent.Intent {
	t.Helper()
	return &intent.Intent{
		Name:       "peer-node",
		Network:    "private",
		Target:     intent.Target{Type: "local", Runtime: "jar"},
		Monitoring: mon,
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051, P2P: 18888, Metrics: 9527},
			InstallPath: filepath.Join(dir, "install"),
		}},
	}
}

// TestApply_PersistsP2PPort guards a field that looks cosmetic and is not.
// `network add` builds the joining node's peer list from the P2PPort of
// every node already in state and SKIPS any entry where it is zero
// (cmd/network/add.go). Apply used to omit the field, so a node deployed
// through Apply was invisible as a peer: the late joiner came up with an
// empty peer list, never connected, and nothing in either command's output
// said why.
func TestApply_PersistsP2PPort(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, st := freshStore(t)
	if _, err := Apply(context.Background(), Options{
		Intent: peerIntent(t, dir, nil), Target: &fakeTarget{},
		Store: store, State: st, IntentHash: "h",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	n := store.GetNode(st, "peer-node")
	if n == nil {
		t.Fatal("node absent from state after a successful apply")
	}
	if n.P2PPort != 18888 {
		t.Errorf("state P2PPort = %d, want 18888 — `network add` skips peers whose "+
			"P2PPort is zero, so this node would be unreachable to a late joiner",
			n.P2PPort)
	}
}

// TestApply_SkipMonitoringLeavesIntentIntact pins the flag `network create`
// relies on. The network deploys ONE stack scraping every node, so each
// per-node Apply must not deploy its own — but Intent.Monitoring has to stay
// set, because RenderHOCON keys the node's metrics auto-enable off it. The
// flag therefore suppresses the deploy without touching the intent.
func TestApply_SkipMonitoringLeavesIntentIntact(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	mon := &intent.Monitoring{Enabled: intent.BoolPtr(true)}
	intent.ApplyMonitoringDefaults(mon)
	in := peerIntent(t, dir, mon)

	store, st := freshStore(t)
	res, err := Apply(context.Background(), Options{
		Intent: in, Target: &fakeTarget{}, Store: store, State: st,
		IntentHash: "h", SkipMonitoring: true,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if len(res.MonitoringEndpoints) != 0 {
		t.Errorf("SkipMonitoring still produced endpoints %v — each node would "+
			"stand up its own stack and the last one would win",
			res.MonitoringEndpoints)
	}
	if res.MonitoringError != "" {
		t.Errorf("SkipMonitoring should be silent, got error %q", res.MonitoringError)
	}
	if in.Monitoring == nil || in.Monitoring.Enabled == nil || !*in.Monitoring.Enabled {
		t.Error("SkipMonitoring must not clear Intent.Monitoring — RenderHOCON " +
			"reads it to auto-enable the node's prometheus port")
	}
	if n := store.GetNode(st, "peer-node"); n != nil && n.Monitoring != nil {
		t.Errorf("state recorded a monitoring stack despite SkipMonitoring: %+v", n.Monitoring)
	}
}

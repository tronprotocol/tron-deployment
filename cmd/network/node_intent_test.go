package network

import (
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func twoNodeIntent() *intent.Intent {
	return &intent.Intent{
		Name:    "twosr",
		Network: "private",
		Target:  intent.Target{Type: "local", Runtime: "docker"},
		Monitoring: &intent.Monitoring{
			Enabled: intent.BoolPtr(true),
		},
		Nodes: []intent.NodeSpec{
			{Type: "witness", Version: "latest", Ports: intent.PortMapping{HTTP: 8090, P2P: 18888}},
			{Type: "fullnode", Version: "latest", Ports: intent.PortMapping{HTTP: 8091, P2P: 18889}},
		},
	}
}

// TestNodeIntent_ProjectsOneNodeAndRenames covers the two things Apply keys
// off: it reads only Nodes[0], and it uses Intent.Name for the compose
// project, the state entry and Result.Name. A projection that sliced without
// renaming would deploy every node under the network's own name and each
// would overwrite the previous one's state entry.
func TestNodeIntent_ProjectsOneNodeAndRenames(t *testing.T) {
	parsed := twoNodeIntent()

	for i, wantName := range []string{"twosr-node0", "twosr-node1"} {
		sub, name, hash, err := nodeIntent(parsed, i)
		if err != nil {
			t.Fatalf("nodeIntent(%d): %v", i, err)
		}
		if name != wantName {
			t.Errorf("name = %q, want %q", name, wantName)
		}
		if sub.Name != wantName {
			t.Errorf("sub.Name = %q, want %q — Apply names the deployment from this", sub.Name, wantName)
		}
		if len(sub.Nodes) != 1 {
			t.Fatalf("sub.Nodes has %d entries, want exactly 1 (Apply only reads Nodes[0])", len(sub.Nodes))
		}
		if sub.Nodes[0].Type != parsed.Nodes[i].Type {
			t.Errorf("sub carries node %q, want %q", sub.Nodes[0].Type, parsed.Nodes[i].Type)
		}
		if hash == "" {
			t.Error("empty intent hash — Apply rejects that in validateOptions")
		}

		// Shared context must survive the projection: these drive target
		// resolution, the HOCON template choice and the metrics auto-enable.
		if sub.Network != parsed.Network {
			t.Errorf("sub.Network = %q, want %q", sub.Network, parsed.Network)
		}
		if sub.Target != parsed.Target {
			t.Errorf("sub.Target = %+v, want %+v", sub.Target, parsed.Target)
		}
		if sub.Monitoring != parsed.Monitoring {
			t.Error("sub lost Monitoring — RenderHOCON keys the metrics auto-enable off it")
		}
	}
}

// TestNodeIntent_HashIsPerNode is the idempotency contract. Apply short
// circuits to no_change when the incoming hash equals the stored one, so a
// hash shared across nodes would make node 1 look unchanged the moment node 0
// had been applied — the whole network would deploy once and then go quiet.
func TestNodeIntent_HashIsPerNode(t *testing.T) {
	parsed := twoNodeIntent()

	_, _, h0, err := nodeIntent(parsed, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, h1, err := nodeIntent(parsed, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h0 == h1 {
		t.Error("both nodes hashed identically; per-node idempotency would collapse")
	}
}

// TestNodeIntent_HashIsStable — same input, same hash. Without this a
// re-run of `network create` reports every node as updated forever.
func TestNodeIntent_HashIsStable(t *testing.T) {
	_, _, a, err := nodeIntent(twoNodeIntent(), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, b, err := nodeIntent(twoNodeIntent(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("hash not stable across calls: %s vs %s", a, b)
	}
}

// TestNodeIntent_HashTracksThatNodeOnly — editing node 1 must not disturb
// node 0's hash, otherwise a one-node change redeploys the whole network.
func TestNodeIntent_HashTracksThatNodeOnly(t *testing.T) {
	before := twoNodeIntent()
	_, _, h0Before, err := nodeIntent(before, 0)
	if err != nil {
		t.Fatal(err)
	}

	after := twoNodeIntent()
	after.Nodes[1].Ports.HTTP = 19999
	_, _, h0After, err := nodeIntent(after, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, h1After, err := nodeIntent(after, 1)
	if err != nil {
		t.Fatal(err)
	}

	if h0Before != h0After {
		t.Error("changing node 1 changed node 0's hash — a single-node edit would redeploy siblings")
	}
	_, _, h1Before, err := nodeIntent(before, 1)
	if err != nil {
		t.Fatal(err)
	}
	if h1Before == h1After {
		t.Error("changing node 1's port did not change its hash — the edit would deploy as no_change")
	}
}

// TestNodeIntent_DoesNotMutateSource guards the shallow copy: runCreate calls
// this once per node off the same parsed intent, so a projection that wrote
// through to the source would corrupt every later node.
func TestNodeIntent_DoesNotMutateSource(t *testing.T) {
	parsed := twoNodeIntent()
	origName := parsed.Name
	origCount := len(parsed.Nodes)

	if _, _, _, err := nodeIntent(parsed, 0); err != nil {
		t.Fatal(err)
	}
	if parsed.Name != origName {
		t.Errorf("source intent renamed to %q", parsed.Name)
	}
	if len(parsed.Nodes) != origCount {
		t.Errorf("source node list resized to %d, want %d", len(parsed.Nodes), origCount)
	}
}

func TestNodeIntent_OutOfRange(t *testing.T) {
	parsed := twoNodeIntent()
	for _, i := range []int{-1, 2, 99} {
		if _, _, _, err := nodeIntent(parsed, i); err == nil {
			t.Errorf("index %d: want an error, got nil", i)
		}
	}
}

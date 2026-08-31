package apply

import (
	"errors"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func planFixture() *intent.Intent {
	return &intent.Intent{Name: "p", Network: "private", Target: intent.Target{Runtime: ""}, Nodes: []intent.NodeSpec{{Type: "fullnode", Version: "4.8.1", Resources: intent.Resources{Memory: "8GB"}}}}
}

func TestPlanLegacyRawHashMatchesNoIntentChange(t *testing.T) {
	i := planFixture()
	raw := []byte("name: p\nnetwork: private\ntarget:\n  type: local\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n")
	existing := &state.ManagedNode{IntentHash: IntentHashFromBytes(raw), ConfigHash: "different", Version: "4.8.1", Status: "running"}
	got, err := Plan(i, existing, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range got.Changes {
		if c.Field == "intent" {
			t.Fatalf("legacy hash produced intent change: %+v", got.Changes)
		}
	}
}

func TestPlanVersionOnlyChangeDowntimeIs60(t *testing.T) {
	i := planFixture()
	base, err := Plan(i, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	existing := &state.ManagedNode{IntentHash: base.IntentHash, ConfigHash: base.ConfigHash, Version: "4.8.0", Status: "running"}
	got, err := Plan(i, existing, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Downtime != 60 {
		t.Fatalf("downtime=%d", got.Downtime)
	}
}

func TestPlanRenderFailureReturnsStructuredError(t *testing.T) {
	i := planFixture()
	i.Network = "not-a-network"
	got, err := Plan(i, nil, nil, "")
	if err == nil || got != nil {
		t.Fatalf("got=%v err=%v", got, err)
	}
	var structured *output.StructuredError
	if !errors.As(err, &structured) {
		t.Fatalf("error type=%T, want *output.StructuredError", err)
	}
	if structured.Code != "RENDER_ERROR" {
		t.Fatalf("error code=%q, want RENDER_ERROR", structured.Code)
	}
}

func TestPlanDefaultsRuntimeToDocker(t *testing.T) {
	got, err := Plan(planFixture(), nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Runtime != "docker" {
		t.Fatalf("runtime=%q", got.Runtime)
	}
}

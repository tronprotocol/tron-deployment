package network

import (
	"errors"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

func TestTargetResolutionFailureIsReportedForEveryMatchingNode(t *testing.T) {
	nodes := []state.ManagedNode{
		{Name: "net-node0", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
		{Name: "net-node1", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
		{Name: "net-node2", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
	}
	key := "ssh|bad|22|u|"
	failures := targetResolutionFailures(nodes, "net-node", "net", key, errors.New("unreachable"))
	if len(failures) != 3 {
		t.Fatalf("failures = %d, want 3", len(failures))
	}
}

func TestCollectNetworkTargetsResolvesBadTargetOnce(t *testing.T) {
	old := resolveTargetForDestroy
	var calls int
	resolveTargetForDestroy = func(*state.ManagedNode) (target.Target, error) {
		calls++
		return nil, errors.New("unreachable")
	}
	defer func() { resolveTargetForDestroy = old }()
	nodes := []state.ManagedNode{
		{Name: "net-node0", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
		{Name: "net-node1", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
		{Name: "net-node2", Target: state.NodeTarget{Type: "ssh", Host: "bad", Port: 22, User: "u"}},
	}
	_, failures, failed := collectNetworkTargets(nodes, "net-node", "net")
	if calls != 1 || len(failures) != 3 || len(failed) != 1 {
		t.Fatalf("calls=%d failures=%d failed_keys=%d, want 1/3/1", calls, len(failures), len(failed))
	}
	seen := map[string]bool{}
	for _, failure := range failures {
		if seen[failure.Name] {
			t.Fatalf("duplicate failure for %s", failure.Name)
		}
		seen[failure.Name] = true
	}
}

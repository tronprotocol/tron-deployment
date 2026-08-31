package cmd

import (
	"context"
	"io"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type rollbackTestTransaction struct{}

func (rollbackTestTransaction) Activate(context.Context) error { return nil }
func (rollbackTestTransaction) Start(context.Context) error    { return nil }
func (rollbackTestTransaction) Rollback(context.Context) error { return nil }
func (rollbackTestTransaction) Cleanup(context.Context) error  { return nil }

type rollbackTestRuntime struct{}

func (rollbackTestRuntime) Deploy(context.Context, runtime.DeployOpts) error { return nil }
func (rollbackTestRuntime) Start(context.Context, string) error              { return nil }
func (rollbackTestRuntime) Stop(context.Context, string) error               { return nil }
func (rollbackTestRuntime) Remove(context.Context, string, bool) error       { return nil }
func (rollbackTestRuntime) Status(context.Context, string) (*runtime.NodeStatus, error) {
	return nil, nil
}
func (rollbackTestRuntime) Logs(context.Context, string, runtime.LogOpts) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}
func (rollbackTestRuntime) PrepareArtifact(context.Context, string, runtime.UpgradeOpts) (runtime.ArtifactTransaction, error) {
	return rollbackTestTransaction{}, nil
}

func TestRunRollbackNetworkRestorePersistsPreAttemptMetadata(t *testing.T) {
	tests := []struct {
		name, previous, wantPrevious string
	}{
		{"preserves previous version", "A0", "A0"},
		{"first upgrade keeps empty previous", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldResolve, oldSave := resolveRollbackNodeContext, saveRollbackState
			t.Cleanup(func() { resolveRollbackNodeContext, saveRollbackState = oldResolve, oldSave })
			node := &state.ManagedNode{Name: "n0", Runtime: "docker", Version: "A", PreviousVersion: tt.previous}
			st := &state.DeploymentState{Nodes: []state.ManagedNode{*node}}
			resolveRollbackNodeContext = func(string) (*nodeContext, error) {
				return &nodeContext{Node: node, State: st, Target: target.NewLocalTarget(), Runtime: rollbackTestRuntime{}}, nil
			}
			var saved state.ManagedNode
			saveRollbackState = func(nc *nodeContext) error { saved = *nc.Node; return nil }
			t.Setenv("TROND_NETWORK_UPGRADE", "1")
			t.Setenv("TROND_NETWORK_UPGRADE_TARGET_VERSION", "A")
			t.Setenv("TROND_NETWORK_UPGRADE_PREVIOUS_VERSION", tt.previous)
			cmd := newCmd()
			if err := runRollback(cmd, []string{"n0"}); err != nil {
				t.Fatal(err)
			}
			if saved.Version != "A" || saved.PreviousVersion != tt.wantPrevious || saved.Status != "running" {
				t.Fatalf("saved state = version %q previous %q status %q", saved.Version, saved.PreviousVersion, saved.Status)
			}
		})
	}
}

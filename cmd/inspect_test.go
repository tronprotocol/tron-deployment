package cmd

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func TestRunInspectLabelFilterNoMatchReturnsNodeNotFound(t *testing.T) {
	t.Setenv("TROND_STATE_DIR", t.TempDir())
	paths.SetBaseDir("")
	defer paths.SetBaseDir("")
	store, err := state.NewStore(statePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{
		{Name: "node-a", Labels: map[string]string{"role": "witness"}},
		{Name: "node-b", Labels: map[string]string{"role": "fullnode"}},
	}}); err != nil {
		t.Fatal(err)
	}

	oldAll, oldNetwork, oldLabels := inspectAll, inspectNetwork, inspectLabelFlags
	defer func() { inspectAll, inspectNetwork, inspectLabelFlags = oldAll, oldNetwork, oldLabels }()
	inspectAll = false
	inspectNetwork = ""
	inspectLabelFlags = []string{"role=missing"}

	cmd := &cobra.Command{}
	cmd.Flags().String("output", "json", "")
	err = runInspect(cmd, nil)
	if err == nil {
		t.Fatal("runInspect succeeded for an empty filter result")
	}
	structured, ok := err.(*output.StructuredError)
	if !ok {
		t.Fatalf("error type = %T, want *output.StructuredError", err)
	}
	if structured.Code != "NODE_NOT_FOUND" {
		t.Fatalf("error code = %q, want NODE_NOT_FOUND", structured.Code)
	}
	if structured.ExitCode != output.ExitGeneralError {
		t.Fatalf("exit code = %d, want %d", structured.ExitCode, output.ExitGeneralError)
	}
	if len(structured.Suggestions) == 0 || structured.Suggestions[len(structured.Suggestions)-1] != "Available nodes: node-a, node-b" {
		t.Fatalf("suggestions = %v, want available node names", structured.Suggestions)
	}
}

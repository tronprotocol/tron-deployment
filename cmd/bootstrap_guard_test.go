package cmd

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

func TestBootstrapRequirePrivateRejectsBeforeTargetResolution(t *testing.T) {
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, []byte("name: prod\nnetwork: mainnet\ntarget:\n  type: ssh\n  host: 192.0.2.1\n  user: root\nnodes:\n  - type: fullnode\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPath, oldFlag := bootstrapIntentPath, guard.FlagValue
	bootstrapIntentPath, guard.FlagValue = p, true
	t.Cleanup(func() { bootstrapIntentPath, guard.FlagValue = oldPath, oldFlag })
	err := runBootstrap(&cobra.Command{}, nil)
	var structured *output.StructuredError
	if err == nil || !errors.As(err, &structured) || structured.Code != "PRIVATE_NETWORK_REQUIRED" {
		t.Fatalf("error=%v", err)
	}
}

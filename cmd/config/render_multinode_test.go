package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunRenderMultiNodeCreatesSeparateDirectories(t *testing.T) {
	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	if err := os.WriteFile(intentPath, []byte("name: net\nnetwork: private\ntarget:\n  type: local\nnodes:\n  - type: fullnode\n    resources: {memory: 4G}\n  - type: fullnode\n    resources: {memory: 4G}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	oldDir, oldNode := renderOutputDir, renderNodeFilter
	defer func() { renderOutputDir, renderNodeFilter = oldDir, oldNode }()
	renderOutputDir, renderNodeFilter = outDir, -1
	cmd := &cobra.Command{}
	cmd.Flags().String("output", "", "")
	if err := runRender(cmd, []string{intentPath}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"node0", "node1"} {
		if _, err := os.Stat(filepath.Join(outDir, node, "net.conf")); err != nil {
			t.Fatalf("%s config: %v", node, err)
		}
	}
}

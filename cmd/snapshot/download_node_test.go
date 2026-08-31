package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func TestRefuseRunningNodeDestinationByName(t *testing.T) {
	seedSnapshotNodeState(t, state.ManagedNode{Name: "jar0", Runtime: "jar", Status: "running", InstallPath: "/srv/tron/jar0"})

	err := refuseRunningNodeDestination("/srv/tron/jar0", "jar0")
	assertRunningNodeError(t, err, "jar0")
}

func TestRefuseRunningNodeDestinationByPath(t *testing.T) {
	root := t.TempDir()
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", StorageRoot: root})

	err := refuseRunningNodeDestination(filepath.Join(root, "output-directory"), "")
	assertRunningNodeError(t, err, "docker0")
}

func TestRefuseRunningNodeDataRootParentAndOutputDirectory(t *testing.T) {
	root := t.TempDir()
	dataMount := filepath.Join(root, "output-directory")
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", StorageRoot: dataMount})

	for _, dest := range []string{root, dataMount} {
		if err := refuseRunningNodeDestination(dest, ""); err == nil {
			t.Errorf("destination %q was allowed", dest)
		}
	}
}

func TestRefuseRunningNodeDatabaseChild(t *testing.T) {
	root := t.TempDir()
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", StorageRoot: root})
	assertRunningNodeError(t, refuseRunningNodeDestination(filepath.Join(root, "database"), ""), "docker0")
}

func TestRefuseErrorNodeDestination(t *testing.T) {
	root := t.TempDir()
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "error", StorageRoot: root})
	assertRunningNodeError(t, refuseRunningNodeDestination(filepath.Join(root, "database"), ""), "docker0")
}

func TestDockerInstallPathIsNotCandidate(t *testing.T) {
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", InstallPath: "/opt/tron"})
	if err := refuseRunningNodeDestination("/opt/tron", ""); err != nil {
		t.Fatalf("docker install path incorrectly blocked: %v", err)
	}
}

func TestLegacyDockerComposeFallback(t *testing.T) {
	base := t.TempDir()
	paths.SetBaseDir(base)
	t.Cleanup(func() { paths.SetBaseDir("") })
	composeDir := filepath.Join(paths.Deployments(), "old0")
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(composeDir, "docker-compose.yaml"), []byte("      - "+root+":/java-tron/output-directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{Name: "old0", Runtime: "docker", Status: "running", InstallPath: "/opt/tron"}}}); err != nil {
		t.Fatal(err)
	}
	assertRunningNodeError(t, refuseRunningNodeDestination(filepath.Join(root, "database"), ""), "old0")
}

func TestLegacyDockerComposeRelativeFallback(t *testing.T) {
	base := t.TempDir()
	paths.SetBaseDir(base)
	t.Cleanup(func() { paths.SetBaseDir("") })
	name := "old-relative"
	composeDir := filepath.Join(paths.Deployments(), name)
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := "      - ./chain:/java-tron/output-directory\n"
	if err := os.WriteFile(filepath.Join(composeDir, "docker-compose.yaml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: name, Runtime: "docker", Status: "running", InstallPath: "/opt/tron",
	}}}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(paths.Deployments(), name, "chain")
	assertRunningNodeError(t, refuseRunningNodeDestination(dest, ""), name)
}

func TestStoragePathAbsoluteMountOnlyBlocksDataMount(t *testing.T) {
	root := t.TempDir()
	mount := filepath.Join(root, "data")
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", StorageRoot: mount})

	if err := refuseRunningNodeDestination(mount, ""); err == nil {
		t.Fatal("storage.path/data destination was allowed")
	}
	if err := refuseRunningNodeDestination(root, ""); err != nil {
		t.Fatalf("storage.path root incorrectly blocked: %v", err)
	}
}

func TestRefuseRunningNodeDestinationSymlinkAndRelativeForms(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "storage-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	seedSnapshotNodeState(t, state.ManagedNode{Name: "docker0", Runtime: "docker", Status: "running", StorageRoot: root})

	for _, dest := range []string{link + "/output-directory/", filepath.Join(root, ".", "output-directory")} {
		if err := refuseRunningNodeDestination(dest, ""); err == nil {
			t.Errorf("destination %q was allowed", dest)
		}
	}
}

func TestAllowStoppedNodeDestination(t *testing.T) {
	root := t.TempDir()
	seedSnapshotNodeState(t, state.ManagedNode{Name: "jar0", Runtime: "jar", Status: "stopped", InstallPath: root})

	if err := refuseRunningNodeDestination(filepath.Join(root, "output-directory"), "jar0"); err != nil {
		t.Fatalf("stopped node rejected: %v", err)
	}
}

func seedSnapshotNodeState(t *testing.T, node state.ManagedNode) {
	t.Helper()
	base := t.TempDir()
	paths.SetBaseDir(base)
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{node}}); err != nil {
		t.Fatal(err)
	}
}

func assertRunningNodeError(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatal("running node destination was allowed")
	}
	if !strings.Contains(err.Error(), "running node") || !strings.Contains(err.Error(), name) {
		t.Fatalf("error = %v, want running node %s", err, name)
	}
	structured, ok := err.(*output.StructuredError)
	if !ok || len(structured.Suggestions) == 0 || structured.Suggestions[0] != "Stop the node first: trond stop "+name {
		t.Fatalf("error = %v, want stop suggestion", err)
	}
}

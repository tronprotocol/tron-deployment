package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/snapshot"
)

func TestSnapshotProcessIdentityRejectsSleep(t *testing.T) {
	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer proc.Process.Kill()
	defer proc.Wait()

	ok, err := snapshotProcessIdentity(proc.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("sleep pid %d incorrectly matched snapshot download", proc.Process.Pid)
	}
}

func TestSnapshotProcessIdentityRejectsSelf(t *testing.T) {
	ok, err := snapshotProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("test process incorrectly matched snapshot download")
	}
}

func TestRunStopMissingPIDRemovesManifest(t *testing.T) {
	base := t.TempDir()
	paths.SetBaseDir(base)
	defer paths.SetBaseDir("")
	jobsDir := paths.SnapshotJobs()
	job := snapshot.Job{
		ID: "gone", PID: 999999, StartedAt: time.Now().UTC(),
		LogPath: filepath.Join(jobsDir, "gone.log"),
	}
	if err := snapshot.WriteJob(jobsDir, job); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	if err := runStop(cmd, []string{"gone"}); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(jobsDir, "gone.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest still exists, stat error = %v", err)
	}
}

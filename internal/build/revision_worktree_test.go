package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// captureSourceRunner records the directory gradle would have run in and
// the contents of file.txt found there, then fails on purpose so the test
// does not have to plant a fake JAR for the promote/validate step.
type captureSourceRunner struct {
	sourcePath string
	fileBody   string
}

var errStopAfterCapture = errors.New("stop after capture")

func (c *captureSourceRunner) RunDockerBuild(_ context.Context,
	sourcePath, _, _ string, _ string, _ []string, _ map[string]string) error {
	c.sourcePath = sourcePath
	if b, err := os.ReadFile(filepath.Join(sourcePath, "file.txt")); err == nil {
		c.fileBody = string(b)
	}
	return errStopAfterCapture
}

// twoCommitRepo returns a repo whose working tree sits on the SECOND commit,
// plus the sha of the first. file.txt is "original\n" at commit one and
// "working-tree\n" at commit two, so the two are trivially distinguishable.
func twoCommitRepo(t *testing.T) (dir, firstSHA string) {
	t.Helper()
	dir = initSmallRepo(t) // file.txt = "original\n", one commit
	firstSHA = gitHEAD(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("working-tree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, dir, "add", "file.txt")
	gitInRepo(t, dir, "commit", "-q", "-m", "second")
	return dir, firstSHA
}

func runCapturingBuild(t *testing.T, req Request) *captureSourceRunner {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	cap := &captureSourceRunner{}
	restore := SetTestRunner(cap)
	t.Cleanup(restore)

	if _, err := Run(context.Background(), req); err == nil {
		t.Fatal("expected the capture runner to abort the build, got nil error")
	}
	return cap
}

// TestBuild_ExplicitRevisionBuildsThatRevision is the guard for the defect
// where build.revision only *labelled* the artifact. Before the fix, gradle
// ran in the caller's working tree while the cache key, the manifest and
// status.build_revision all reported the pinned sha — so `trond build
// --revision <base>` and `--revision <head>` could hand back byte-identical
// artifacts under two different labels, and a base-vs-head comparison would
// silently compare an artifact against itself.
func TestBuild_ExplicitRevisionBuildsThatRevision(t *testing.T) {
	dir, firstSHA := twoCommitRepo(t)

	cap := runCapturingBuild(t, Request{
		SourcePath:   dir,
		RevisionSpec: firstSHA,
		ArtifactKind: "jar",
		Builder:      "docker",
	})

	if cap.fileBody != "original\n" {
		t.Errorf("gradle source tree held file.txt = %q, want %q — an explicit "+
			"build.revision must be checked out, not just recorded",
			cap.fileBody, "original\n")
	}
	if cap.sourcePath == dir {
		t.Errorf("build ran in the caller's working tree (%s); an explicit "+
			"revision must build in an isolated worktree", dir)
	}
}

// TestBuild_HeadStillBuildsWorkingTree pins the other half of the contract:
// revision HEAD is the dev inner loop and must keep building the working
// tree, dirty edits included. Source.Resolve folds that dirty state into the
// cache key, so correctness does not depend on a checkout here.
func TestBuild_HeadStillBuildsWorkingTree(t *testing.T) {
	dir, _ := twoCommitRepo(t)

	// Uncommitted edit on top of the second commit.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cap := runCapturingBuild(t, Request{
		SourcePath:   dir,
		RevisionSpec: "HEAD",
		ArtifactKind: "jar",
		Builder:      "docker",
	})

	if cap.fileBody != "dirty\n" {
		t.Errorf("gradle source tree held file.txt = %q, want %q — HEAD must "+
			"build the working tree so the dev inner loop still sees edits",
			cap.fileBody, "dirty\n")
	}
	if cap.sourcePath != dir {
		t.Errorf("HEAD build ran in %s, want the source tree %s — a worktree "+
			"here would drop uncommitted edits", cap.sourcePath, dir)
	}
}

// TestBuild_ExplicitBranchBuildsThatBranch covers the non-sha spelling; the
// schema documents branch and tag alongside sha.
func TestBuild_ExplicitBranchBuildsThatBranch(t *testing.T) {
	dir, firstSHA := twoCommitRepo(t)
	gitInRepo(t, dir, "branch", "pinned", firstSHA)

	cap := runCapturingBuild(t, Request{
		SourcePath:   dir,
		RevisionSpec: "pinned",
		ArtifactKind: "jar",
		Builder:      "docker",
	})

	if cap.fileBody != "original\n" {
		t.Errorf("branch revision built file.txt = %q, want %q",
			cap.fileBody, "original\n")
	}
}

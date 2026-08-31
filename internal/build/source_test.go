package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initGitRepo creates a fresh git repository with one commit and
// returns its absolute path. The fixture is intentionally tiny —
// these tests exercise trond's git wrapper, not gradle.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init")
	mustGit(t, dir, "config", "user.email", "trond-test@example.com")
	mustGit(t, dir, "config", "user.name", "trond test")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, dir, "add", "README.md")
	mustGit(t, dir, "commit", "-m", "initial")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSource_Resolve_CleanRepo(t *testing.T) {
	dir := initGitRepo(t)
	s := Source{Path: dir, RevisionSpec: "HEAD"}
	if err := s.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(s.ResolvedRevision) != 40 {
		t.Errorf("ResolvedRevision %q is not a full 40-char sha", s.ResolvedRevision)
	}
	if s.DirtyState {
		t.Error("clean repo should not be marked dirty")
	}
	if s.PatchHash != "" {
		t.Errorf("clean repo should have empty PatchHash; got %q", s.PatchHash)
	}
}

// TestSource_Resolve_DirtyWithUntracked is the regression guard for
// FR-002: an untracked file MUST invalidate the cache (was missing
// from the v1 design — `git diff` alone misses untracked files).
func TestSource_Resolve_DirtyWithUntracked(t *testing.T) {
	dir := initGitRepo(t)

	s1 := Source{Path: dir, RevisionSpec: "HEAD"}
	if err := s1.Resolve(context.Background()); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	cleanHash := s1.PatchHash

	// Add an untracked file. git diff returns empty here; trond MUST
	// notice via git status --porcelain -uall.
	if err := os.WriteFile(filepath.Join(dir, "NEWFILE.java"),
		[]byte("class C {}\n"), 0o600); err != nil {
		t.Fatalf("write NEWFILE: %v", err)
	}

	s2 := Source{Path: dir, RevisionSpec: "HEAD"}
	if err := s2.Resolve(context.Background()); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !s2.DirtyState {
		t.Fatal("repo with untracked file MUST be marked dirty (FR-002)")
	}
	if s2.PatchHash == "" {
		t.Fatal("dirty repo MUST have non-empty PatchHash")
	}
	if s2.PatchHash == cleanHash {
		t.Fatal("adding an untracked file MUST change PatchHash")
	}
}

func TestSource_Resolve_UntrackedContentChangesHash(t *testing.T) {
	dir := initGitRepo(t)
	path := filepath.Join(dir, "new.go")
	if err := os.WriteFile(path, []byte("package first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := resolveSource(t, dir).PatchHash
	if err := os.WriteFile(path, []byte("package second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := resolveSource(t, dir).PatchHash
	if first == second {
		t.Fatalf("untracked content change did not change hash: %q", first)
	}
}

func TestSource_Resolve_NoUntrackedHashUnchanged(t *testing.T) {
	dir := initGitRepo(t)
	first := resolveSource(t, dir).PatchHash
	second := resolveSource(t, dir).PatchHash
	if first != second || first != "" {
		t.Fatalf("clean hash changed: %q then %q", first, second)
	}
}

func TestSource_Resolve_IgnoredUntrackedDoesNotChangeHash(t *testing.T) {
	dir := initGitRepo(t)
	var baseline string
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", ".gitignore")
	mustGit(t, dir, "commit", "-m", "ignore generated files")
	baseline = resolveSource(t, dir).PatchHash
	if err := os.Mkdir(filepath.Join(dir, "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignored", "output.bin"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveSource(t, dir).PatchHash
	if got != baseline {
		t.Fatalf("ignored file changed hash: %q then %q", baseline, got)
	}
}

func resolveSource(t *testing.T, dir string) Source {
	t.Helper()
	s := Source{Path: dir, RevisionSpec: "HEAD"}
	if err := s.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return s
}

func TestSource_Resolve_DirtyTrackedEdit(t *testing.T) {
	dir := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte("changed\n"), 0o600); err != nil {
		t.Fatalf("modify README: %v", err)
	}
	s := Source{Path: dir, RevisionSpec: "HEAD"}
	if err := s.Resolve(context.Background()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !s.DirtyState {
		t.Error("modified tracked file should be dirty")
	}
}

func TestSource_Resolve_NonExistentRevision(t *testing.T) {
	dir := initGitRepo(t)
	s := Source{Path: dir, RevisionSpec: "does-not-exist-branch"}
	err := s.Resolve(context.Background())
	if err == nil {
		t.Error("unknown revision should produce an error")
	}
}

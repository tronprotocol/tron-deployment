package target

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalTargetWriteFileTightensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewLocalTarget().WriteFile(context.Background(), path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
}

func TestLocalTargetDownloadTightensExistingFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewLocalTarget().Download(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
}

// TestLocalTarget_PutFile_DifferentPath asserts the standard copy
// flow with atomic install (write `.tmp`, rename). Phase 4 needs
// this when the cobra path serves a Local target but the caller
// pretends to "transfer" between local paths (used for symmetry
// with the SSH branch).
func TestLocalTarget_PutFile_DifferentPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jar")
	dst := filepath.Join(dir, "subdir", "dst.jar")

	body := []byte("ship it")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}

	tgt := NewLocalTarget()
	if err := tgt.PutFile(context.Background(), src, dst); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("dst contents mismatch: got %q want %q", got, body)
	}
	// .tmp file should be gone (atomic rename completed)
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file should not survive PutFile: stat err=%v", err)
	}
}

// TestLocalTarget_PutFile_SamePath is the canonical Phase 4 dev-
// loop case: trond's build pipeline already placed the artifact at
// the cache path, and apply's "transfer" is a no-op when target is
// Local. Asserting it doesn't corrupt the file (no truncate, no
// touch).
func TestLocalTarget_PutFile_SamePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.jar")
	body := []byte("don't touch me")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(p)

	tgt := NewLocalTarget()
	if err := tgt.PutFile(context.Background(), p, p); err != nil {
		t.Fatalf("PutFile same path: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(body) {
		t.Errorf("file should be untouched; got %q", got)
	}
	statAfter, _ := os.Stat(p)
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("file mtime should not have changed on same-path PutFile: before %v, after %v",
			statBefore.ModTime(), statAfter.ModTime())
	}
}

// TestLocalTarget_Sha256IfExists_MatchesAndMissing covers both
// branches: present file returns the hex; missing returns "".
func TestLocalTarget_Sha256IfExists_MatchesAndMissing(t *testing.T) {
	dir := t.TempDir()
	tgt := NewLocalTarget()
	ctx := context.Background()

	missing := filepath.Join(dir, "no-such-file")
	got, err := tgt.Sha256IfExists(ctx, missing)
	if err != nil {
		t.Errorf("missing file should NOT error; got %v", err)
	}
	if got != "" {
		t.Errorf("missing file should return empty sha; got %q", got)
	}

	present := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(present, []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = tgt.Sha256IfExists(ctx, present)
	if err != nil {
		t.Fatalf("Sha256IfExists: %v", err)
	}
	// sha256("hi\n") = 98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4
	const want = "98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4"
	if got != want {
		t.Errorf("sha = %q; want %q", got, want)
	}
}

// TestLocalTarget_CommandExists smoke-checks the helper against a
// command that definitely exists (sh, on any POSIX system) and one
// that doesn't.
func TestLocalTarget_CommandExists(t *testing.T) {
	tgt := NewLocalTarget()
	ctx := context.Background()

	if !tgt.CommandExists(ctx, "sh") {
		t.Error("sh should exist on POSIX systems")
	}
	if tgt.CommandExists(ctx, "definitely-not-a-real-command-zzz") {
		t.Error("nonexistent command should return false")
	}
}

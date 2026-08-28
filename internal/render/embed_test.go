package render

import (
	"os"
	"path/filepath"
	"testing"
)

// FindTemplatesDir is the single rule every command resolves the on-disk
// template override through. It used to be copy-pasted into three cmd
// packages with three different notions of what a templates dir is; these
// tests pin the one rule so a future edit can't quietly re-fork it.
func TestFindTemplatesDir(t *testing.T) {
	// chdir into a scratch dir so a real ./templates in the repo root
	// (the private_net_config.conf symlink) can't leak into these cases.
	chdirTemp := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		t.Chdir(dir)
		return dir
	}

	t.Run("env var wins outright", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("TROND_TEMPLATES_DIR", "/somewhere/else")
		if got := FindTemplatesDir(); got != "/somewhere/else" {
			t.Fatalf("got %q, want /somewhere/else", got)
		}
	})

	t.Run("no templates dir means embedded", func(t *testing.T) {
		chdirTemp(t)
		t.Setenv("TROND_TEMPLATES_DIR", "")
		if got := FindTemplatesDir(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("dir without a known template means embedded", func(t *testing.T) {
		dir := chdirTemp(t)
		t.Setenv("TROND_TEMPLATES_DIR", "")
		if err := os.Mkdir(filepath.Join(dir, localTemplatesDir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, localTemplatesDir, "notes.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := FindTemplatesDir(); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	// Any single known template is enough: keying off one particular
	// network would ignore a directory that only carries the others.
	for network, file := range NetworkTemplate {
		t.Run("dir carrying only "+network, func(t *testing.T) {
			dir := chdirTemp(t)
			t.Setenv("TROND_TEMPLATES_DIR", "")
			if err := os.Mkdir(filepath.Join(dir, localTemplatesDir), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, localTemplatesDir, file), []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := FindTemplatesDir(); got != localTemplatesDir {
				t.Fatalf("got %q, want %q", got, localTemplatesDir)
			}
		})
	}
}

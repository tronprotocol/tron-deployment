package build

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFoldUntrackedFileToleratesSpecialEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested.git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unreadable"), []byte("secret"), 0000); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"nested.git", "dangling", "unreadable"} {
		var hash bytes.Buffer
		if err := foldUntrackedFile(&hash, i, dir, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if hash.Len() == 0 {
			t.Fatalf("%s produced no hash marker", name)
		}
	}
}

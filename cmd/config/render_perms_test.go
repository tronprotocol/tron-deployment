package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The rendered HOCON carries the witness signing key inlined by
// render.RenderHOCON, so `config render --output-dir` must never leave it
// group/world readable. These tests pin the modes of every artifact
// writeRenderedFiles produces, and — crucially — pin them for the
// re-render case too: os.WriteFile's perm argument applies only on
// creation, so writing over a .conf an earlier run left at 0644 has to
// actively tighten the mode.

const (
	testHOCON   = "# rendered\nlocalwitness = [\"deadbeef\"]\n"
	testCompose = "services:\n  node:\n    image: x\n"
	testSystemd = "[Unit]\nDescription=x\n"
)

func skipIfNoUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not meaningful on Windows")
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// A fresh output dir: the directory is created 0700 and the secret-bearing
// .conf 0600, while the non-secret runtime artifacts keep 0644.
func TestWriteRenderedFiles_FreshDirModes(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := filepath.Join(t.TempDir(), "out")

	if err := writeRenderedFiles(dir, "my-witness", testHOCON, testCompose, testSystemd); err != nil {
		t.Fatalf("writeRenderedFiles: %v", err)
	}

	if got := mode(t, dir); got != 0700 {
		t.Errorf("output dir mode = %04o; want 0700", got)
	}
	conf := filepath.Join(dir, "my-witness.conf")
	if got := mode(t, conf); got != 0600 {
		t.Errorf("%s mode = %04o; want 0600", conf, got)
	}
	for _, name := range []string{"docker-compose.yaml", "tron-my-witness.service"} {
		if got := mode(t, filepath.Join(dir, name)); got != 0644 {
			t.Errorf("%s mode = %04o; want 0644", name, got)
		}
	}

	// The bodies must be byte-identical to what was rendered.
	if got := mustRead(t, conf); got != testHOCON {
		t.Errorf("hocon body = %q; want %q", got, testHOCON)
	}
	if got := mustRead(t, filepath.Join(dir, "docker-compose.yaml")); got != testCompose {
		t.Errorf("compose body = %q; want %q", got, testCompose)
	}
	if got := mustRead(t, filepath.Join(dir, "tron-my-witness.service")); got != testSystemd {
		t.Errorf("systemd body = %q; want %q", got, testSystemd)
	}
}

// The regression this fix is really about: an operator re-renders into a
// stable --output-dir that already holds a .conf left world-readable by an
// earlier (pre-fix) run. The mode argument to a create-if-missing write is
// ignored for an existing file, so the write must tighten it explicitly.
func TestWriteRenderedFiles_PreExistingConfIsTightened(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "my-witness.conf")

	// Simulate the artifact a pre-fix trond left behind: 0644, and longer
	// than the new body so a missing truncate would show up as leftovers.
	stale := "# stale render\nlocalwitness = [\"0000\"]\n" + string(make([]byte, 512))
	if err := os.WriteFile(conf, []byte(stale), 0644); err != nil {
		t.Fatalf("seed conf: %v", err)
	}
	if got := mode(t, conf); got != 0644 {
		t.Fatalf("seeded conf mode = %04o; want 0644 (umask interfered)", got)
	}

	if err := writeRenderedFiles(dir, "my-witness", testHOCON, "", ""); err != nil {
		t.Fatalf("writeRenderedFiles: %v", err)
	}

	if got := mode(t, conf); got != 0600 {
		t.Fatalf("re-rendered conf mode = %04o; want 0600 — the witness key is still readable by other local accounts", got)
	}
	if got := mustRead(t, conf); got != testHOCON {
		t.Errorf("re-rendered body = %q; want %q (stale content must be truncated)", got, testHOCON)
	}
}

// Even a conf that somehow ended up group/world *writable* is brought back
// to 0600 by a re-render.
func TestWriteRenderedFiles_PreExistingConfWorldWritable(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := t.TempDir()
	conf := filepath.Join(dir, "n.conf")
	if err := os.WriteFile(conf, []byte("old"), 0666); err != nil {
		t.Fatalf("seed conf: %v", err)
	}
	if err := os.Chmod(conf, 0666); err != nil { // defeat the umask
		t.Fatalf("chmod seed: %v", err)
	}

	if err := writeRenderedFiles(dir, "n", testHOCON, "", ""); err != nil {
		t.Fatalf("writeRenderedFiles: %v", err)
	}
	if got := mode(t, conf); got != 0600 {
		t.Errorf("conf mode = %04o; want 0600", got)
	}
}

// Rendering twice in a row keeps the tight mode (and does not, e.g., widen
// it on the second pass).
func TestWriteRenderedFiles_ReRenderKeepsTightMode(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := filepath.Join(t.TempDir(), "out")

	for i := 0; i < 2; i++ {
		if err := writeRenderedFiles(dir, "w", testHOCON, testCompose, ""); err != nil {
			t.Fatalf("writeRenderedFiles pass %d: %v", i, err)
		}
	}
	if got := mode(t, filepath.Join(dir, "w.conf")); got != 0600 {
		t.Errorf("conf mode after re-render = %04o; want 0600", got)
	}
	if got := mode(t, filepath.Join(dir, "docker-compose.yaml")); got != 0644 {
		t.Errorf("compose mode after re-render = %04o; want 0644", got)
	}
}

// A directory the operator already created keeps its own mode — we only
// promise that the file inside it is 0600. Changing a path the caller
// chose (and may share deliberately) is not this function's business.
func TestWriteRenderedFiles_ExistingDirModeUntouched(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil { // defeat the umask
		t.Fatalf("chmod: %v", err)
	}

	if err := writeRenderedFiles(dir, "w", testHOCON, "", ""); err != nil {
		t.Fatalf("writeRenderedFiles: %v", err)
	}
	if got := mode(t, dir); got != 0755 {
		t.Errorf("existing dir mode = %04o; want it left at 0755", got)
	}
	if got := mode(t, filepath.Join(dir, "w.conf")); got != 0600 {
		t.Errorf("conf mode = %04o; want 0600", got)
	}
}

// writeSecretFile reports errors instead of silently leaving a file at a
// loose mode; the unwritable-path case is the cheapest way to exercise it.
func TestWriteSecretFile_ErrorPropagates(t *testing.T) {
	skipIfNoUnixModes(t)
	dir := t.TempDir()
	if err := writeSecretFile(filepath.Join(dir, "missing-subdir", "x.conf"), []byte("x")); err == nil {
		t.Fatal("writeSecretFile into a nonexistent directory: want error, got nil")
	}
}

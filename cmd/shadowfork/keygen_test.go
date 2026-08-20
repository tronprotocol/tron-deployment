package shadowfork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/dbfork"
)

// scripts/poc-shadow-fork.sh sources this file, so the shape of what
// --out writes is a contract: two shell exports, and a mode the key can
// live at.
func TestKeygenOutFileIsSourceableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "witness.env")

	kgOutPath = path
	t.Cleanup(func() { kgOutPath = "" })
	if err := runKeygen(keygenCmd, nil); err != nil {
		t.Fatalf("runKeygen: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file is mode %o, want 600", perm)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(blob)
	for _, want := range []string{"export SHADOW_FORK_WITNESS_KEY=", "export SHADOW_FORK_WITNESS_ADDRESS="} {
		if !strings.Contains(body, want) {
			t.Errorf("output is missing %q — the script sources this file:\n%s", want, body)
		}
	}

	// The address has to be one java-tron will accept, so run it back
	// through the decoder the mutate path uses.
	addr := valueOf(t, body, "SHADOW_FORK_WITNESS_ADDRESS")
	if _, err := dbfork.DecodeAddress(addr); err != nil {
		t.Errorf("generated address %q does not decode: %v", addr, err)
	}
	if key := valueOf(t, body, "SHADOW_FORK_WITNESS_KEY"); len(key) != 64 {
		t.Errorf("private key is %d hex chars, want 64", len(key))
	}
}

func valueOf(t *testing.T, body, name string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if after, ok := strings.CutPrefix(line, "export "+name+"="); ok {
			return strings.Trim(after, `"`)
		}
	}
	t.Fatalf("no %s in:\n%s", name, body)
	return ""
}

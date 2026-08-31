package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, path
}

func TestLoad_EmptyFile(t *testing.T) {
	s, _ := newTempStore(t)
	st, err := s.Load()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if st.Version != stateFileVersion {
		t.Errorf("Version = %d, want %d", st.Version, stateFileVersion)
	}
	if len(st.Nodes) != 0 {
		t.Errorf("want empty nodes, got %d", len(st.Nodes))
	}
}

func TestSaveIgnoresDirectorySyncFailureAfterRename(t *testing.T) {
	s, path := newTempStore(t)
	old := openStateDir
	openStateDir = func(string) (stateDir, error) { return failingStateDir{}, nil }
	t.Cleanup(func() { openStateDir = old })
	if err := s.Save(&DeploymentState{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("renamed state missing: %v", err)
	}
}

type failingStateDir struct{}

func (failingStateDir) Sync() error  { return os.ErrInvalid }
func (failingStateDir) Close() error { return nil }

func TestUpsertAndGetNode(t *testing.T) {
	s, _ := newTempStore(t)
	st, _ := s.Load()

	n := ManagedNode{Name: "alpha", Version: "v1", Status: "running", LastApplied: time.Now().UTC()}
	s.UpsertNode(st, n)
	if len(st.Nodes) != 1 {
		t.Fatalf("want 1 node, got %d", len(st.Nodes))
	}

	got := s.GetNode(st, "alpha")
	if got == nil || got.Version != "v1" {
		t.Errorf("get alpha = %+v", got)
	}

	// Upsert updates in place rather than appending
	n.Version = "v2"
	s.UpsertNode(st, n)
	if len(st.Nodes) != 1 {
		t.Errorf("upsert duplicated: got %d nodes", len(st.Nodes))
	}
	if s.GetNode(st, "alpha").Version != "v2" {
		t.Error("version not updated")
	}
}

func TestRemoveNode(t *testing.T) {
	s, _ := newTempStore(t)
	st, _ := s.Load()
	s.UpsertNode(st, ManagedNode{Name: "a"})
	s.UpsertNode(st, ManagedNode{Name: "b"})

	if !s.RemoveNode(st, "a") {
		t.Error("remove returned false")
	}
	if len(st.Nodes) != 1 || st.Nodes[0].Name != "b" {
		t.Errorf("after remove: %+v", st.Nodes)
	}
	if s.RemoveNode(st, "missing") {
		t.Error("remove of missing node returned true")
	}
}

func TestSaveAtomic(t *testing.T) {
	s, path := newTempStore(t)
	st, _ := s.Load()
	s.UpsertNode(st, ManagedNode{Name: "atomic", Version: "v1"})

	if err := s.Save(st); err != nil {
		t.Fatalf("save: %v", err)
	}

	// No staging file should be left behind.
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state-*.tmp")); err != nil || len(matches) != 0 {
		t.Errorf("temporary state files remain: matches=%v err=%v", matches, err)
	}

	// Reload and verify contents round-trip
	s2, _ := NewStore(path)
	loaded, err := s2.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].Name != "atomic" {
		t.Errorf("round-trip mismatch: %+v", loaded.Nodes)
	}
}

func TestSave_ErrorPropagatesAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	// A directory at the destination makes the final rename fail.
	path := filepath.Join(dir, "state.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&DeploymentState{Nodes: []ManagedNode{{Name: "broken"}}}); err == nil {
		t.Fatal("Save succeeded despite destination being a directory")
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".state-*.tmp")); err != nil || len(matches) != 0 {
		t.Errorf("temporary state files remain after failure: matches=%v err=%v", matches, err)
	}
}

func TestSave_ReplacesWithCompleteJSON(t *testing.T) {
	s, path := newTempStore(t)
	if err := s.Save(&DeploymentState{Nodes: []ManagedNode{{Name: "complete"}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded DeploymentState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("final state is not complete JSON: %v", err)
	}
	if len(loaded.Nodes) != 1 || loaded.Nodes[0].Name != "complete" {
		t.Fatalf("unexpected final state: %+v", loaded)
	}
}

func TestSave_PermissionsRestrictive(t *testing.T) {
	s, path := newTempStore(t)
	st, _ := s.Load()
	s.UpsertNode(st, ManagedNode{Name: "perm"})
	if err := s.Save(st); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// State file holds target hosts and identity file paths — must not be
	// world-readable.
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("state file perms too open: %o", info.Mode().Perm())
	}
}

func TestHasChanged(t *testing.T) {
	s, _ := newTempStore(t)
	existing := &ManagedNode{IntentHash: "a", ConfigHash: "b"}

	if s.HasChanged(existing, "a", "b") {
		t.Error("identical hashes should report no change")
	}
	if !s.HasChanged(existing, "c", "b") {
		t.Error("intent hash change not detected")
	}
	if !s.HasChanged(existing, "a", "d") {
		t.Error("config hash change not detected")
	}
}

func TestLoad_InvalidJSONReturnsError(t *testing.T) {
	s, path := newTempStore(t)
	if err := os.WriteFile(path, []byte("{ not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Error("expected parse error for invalid JSON")
	}
}

func TestSave_WritesVersion(t *testing.T) {
	s, path := newTempStore(t)
	st := &DeploymentState{Nodes: []ManagedNode{{Name: "v"}}}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	if raw["version"] == nil {
		t.Error("saved state missing version field")
	}
}

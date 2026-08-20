package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The lock has to serialise a whole load-modify-save cycle. Without it the
// two goroutines below both read the same list, each append their own node,
// and whichever saves last wins — the other node is gone.
func TestLockSerialisesLoadModifySave(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	var wg sync.WaitGroup
	for _, name := range []string{"node-a", "node-b"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			lock := NewLock(dir)
			if err := lock.Acquire(); err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer lock.Release()

			st, err := store.Load()
			if err != nil {
				t.Errorf("load: %v", err)
				return
			}
			store.UpsertNode(st, ManagedNode{Name: n})
			if err := store.Save(st); err != nil {
				t.Errorf("save: %v", err)
			}
		}(name)
	}
	wg.Wait()

	st, err := store.Load()
	if err != nil {
		t.Fatalf("final load: %v", err)
	}
	if len(st.Nodes) != 2 {
		t.Fatalf("lost an update: want 2 nodes, got %d (%+v)", len(st.Nodes), st.Nodes)
	}
}

// Save must not leave temp files behind, and must not collide when two
// writers run at once — a fixed ".tmp" name would have them overwrite each
// other's partially written file.
func TestSaveUsesUniqueTempFiles(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := &DeploymentState{Nodes: []ManagedNode{{Name: "n"}}}
			if err := store.Save(st); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("leftover file after Save: %s", e.Name())
		}
	}
}

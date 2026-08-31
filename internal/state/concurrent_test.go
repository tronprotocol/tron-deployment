package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLock_TryAcquireBlocksWhenHeld pins the contract that
// TryAcquire returns (false, nil) immediately when another holder
// has the lock — without blocking. Used by tools that want to fail
// fast on lock contention rather than waiting (e.g. status / list
// when the agent has already detected an apply in flight).
func TestLock_TryAcquireBlocksWhenHeld(t *testing.T) {
	dir := t.TempDir()
	a := NewLock(dir)
	b := NewLock(dir)

	if err := a.Acquire(); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	defer a.Release()

	got, err := b.TryAcquire()
	if err != nil {
		t.Fatalf("b.TryAcquire: %v", err)
	}
	if got {
		t.Fatal("b.TryAcquire returned true while a holds the lock")
	}
}

// TestLock_AcquireWaitsForRelease verifies that a second Acquire()
// blocks until the first holder calls Release. This is the canonical
// "two trond apply against the same state" behaviour: the second one
// queues, doesn't fail.
func TestLock_AcquireWaitsForRelease(t *testing.T) {
	dir := t.TempDir()
	a := NewLock(dir)
	b := NewLock(dir)

	if err := a.Acquire(); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}

	bAcquired := atomic.Bool{}
	bDone := make(chan struct{})
	go func() {
		defer close(bDone)
		if err := b.Acquire(); err != nil {
			t.Errorf("b.Acquire: %v", err)
			return
		}
		bAcquired.Store(true)
		_ = b.Release()
	}()

	// Give b a moment to attempt the acquire and confirm it's blocked.
	time.Sleep(150 * time.Millisecond)
	if bAcquired.Load() {
		t.Fatal("b.Acquire returned before a released — flock not enforced")
	}

	if err := a.Release(); err != nil {
		t.Fatalf("a.Release: %v", err)
	}

	select {
	case <-bDone:
		if !bAcquired.Load() {
			t.Fatal("b.Acquire returned without setting flag — Acquire path is broken")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("b.Acquire didn't unblock 2s after a.Release")
	}
}

// TestStore_ConcurrentSavesDontDropData hammers Save from many
// goroutines (each acquiring the lock) and verifies the final on-disk
// state contains every node we wrote. Lost-update prevention belongs to the
// caller's state.lock protocol; this test covers that protocol plus Save's
// complete-file writes.
func TestStore_ConcurrentSavesDontDropData(t *testing.T) {
	dir := t.TempDir()
	storePath := dir + "/state.json"

	const writers = 10
	const writesPerGoroutine = 5

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := range writesPerGoroutine {
				lock := NewLock(dir)
				if err := lock.Acquire(); err != nil {
					t.Errorf("Acquire: %v", err)
					return
				}
				store, err := NewStore(storePath)
				if err != nil {
					_ = lock.Release()
					t.Errorf("NewStore: %v", err)
					return
				}
				st, err := store.Load()
				if err != nil {
					_ = lock.Release()
					t.Errorf("Load: %v", err)
					return
				}
				name := stableName(id, j)
				store.UpsertNode(st, ManagedNode{Name: name, Status: "running"})
				if err := store.Save(st); err != nil {
					_ = lock.Release()
					t.Errorf("Save: %v", err)
					return
				}
				_ = lock.Release()
			}
		}(i)
	}
	wg.Wait()

	// Final read: every (id, j) name must be present.
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatalf("final NewStore: %v", err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatalf("final Load: %v", err)
	}
	got := map[string]bool{}
	for _, n := range st.Nodes {
		got[n.Name] = true
	}
	for i := range writers {
		for j := range writesPerGoroutine {
			if !got[stableName(i, j)] {
				t.Errorf("missing node %s — Save lost data under contention", stableName(i, j))
			}
		}
	}
}

func TestStore_ConcurrentSavesWithoutLockKeepCompleteJSON(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"
	const writers = 20

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			store, err := NewStore(path)
			if err != nil {
				t.Errorf("NewStore: %v", err)
				return
			}
			if err := store.Save(&DeploymentState{Nodes: []ManagedNode{{Name: stableName(id, 0)}}}); err != nil {
				t.Errorf("Save: %v", err)
			}
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got DeploymentState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final state is corrupted: %v", err)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("final state has %d nodes, want one complete candidate", len(got.Nodes))
	}
	for i := range writers {
		if got.Nodes[0].Name == stableName(i, 0) {
			goto candidateFound
		}
	}
	t.Fatalf("final state %q is not one of the concurrent Save candidates", got.Nodes[0].Name)

candidateFound:
	if matches, err := filepath.Glob(filepath.Join(dir, ".state-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary state files remain: matches=%v err=%v", matches, err)
	}
}

func stableName(id, j int) string {
	return "node-" + itoa(id) + "-" + itoa(j)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

package apply

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// fakeTarget satisfies target.Target with no-op stubs. Apply only
// exercises Exec (for the JDK probe + docker deploy fan-out); the
// other methods exist to satisfy the interface contract.
type fakeTarget struct {
	chmodPaths []string
	chmodPerms []os.FileMode
}

func (f *fakeTarget) Exec(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, nil
}
func (f *fakeTarget) Upload(_ context.Context, _, _ string) error          { return nil }
func (f *fakeTarget) Download(_ context.Context, _, _ string) error        { return nil }
func (f *fakeTarget) ReadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (f *fakeTarget) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode) error {
	return nil
}
func (f *fakeTarget) Chmod(_ context.Context, path string, perm os.FileMode) error {
	f.chmodPaths = append(f.chmodPaths, path)
	f.chmodPerms = append(f.chmodPerms, perm)
	return nil
}
func (f *fakeTarget) DiskFree(_ context.Context, _ string) (uint64, error) { return 1 << 40, nil }
func (f *fakeTarget) MemTotal(_ context.Context) (uint64, error)           { return 1 << 30, nil }
func (f *fakeTarget) PutFile(_ context.Context, _, _ string) error         { return nil }
func (f *fakeTarget) Sha256IfExists(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (f *fakeTarget) CommandExists(_ context.Context, _ string) bool { return true }
func (f *fakeTarget) String() string                                 { return "fake" }

func TestApply_NoOpWhenIntentHashMatches(t *testing.T) {
	parsed := minimalIntent()
	store, st := freshStore(t)
	existing := state.ManagedNode{
		Name:        parsed.Name,
		IntentHash:  "deadbeef",
		ConfigHash:  "cafef00d",
		Version:     "4.8.1",
		Runtime:     "docker",
		Status:      "running",
		LastApplied: time.Now(),
	}
	store.UpsertNode(st, existing)

	res, err := Apply(context.Background(), Options{
		Intent:     parsed,
		Target:     &fakeTarget{},
		Store:      store,
		State:      st,
		IntentHash: "deadbeef",
		Existing:   &existing,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != "no_change" {
		t.Errorf("Outcome = %s, want no_change", res.Outcome)
	}
	if res.Name != parsed.Name {
		t.Errorf("Name = %s, want %s", res.Name, parsed.Name)
	}
}

func TestRemediateJarConfigPermissions(t *testing.T) {
	tests := []struct {
		name      string
		runtime   string
		install   string
		wantChmod bool
	}{
		{name: "jar", runtime: "jar", install: "/opt/tron", wantChmod: true},
		{name: "non-jar", runtime: "docker", install: "/opt/tron"},
		{name: "empty install path", runtime: "jar"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTarget{}
			parsed := minimalIntent()
			parsed.Target.Runtime = tc.runtime
			parsed.Nodes[0].InstallPath = tc.install
			existing := state.ManagedNode{Runtime: tc.runtime}
			remediateJarConfigPermissions(context.Background(), Options{
				Target:   ft,
				Existing: &existing,
			}, &parsed.Nodes[0])
			if tc.wantChmod {
				if len(ft.chmodPaths) != 1 || ft.chmodPaths[0] != "/opt/tron/config.conf" {
					t.Fatalf("Chmod calls = %v, want /opt/tron/config.conf", ft.chmodPaths)
				}
				if ft.chmodPerms[0] != 0o600 {
					t.Errorf("Chmod mode = %#o, want 0600", ft.chmodPerms[0])
				}
			} else if len(ft.chmodPaths) != 0 {
				t.Fatalf("unexpected Chmod calls: %v", ft.chmodPaths)
			}
		})
	}
}

func TestApply_NoChangeRemediatesJarConfigPermissions(t *testing.T) {
	parsed := minimalIntent()
	parsed.Target.Runtime = "jar"
	parsed.Nodes[0].InstallPath = "/opt/tron"
	store, st := freshStore(t)
	ft := &fakeTarget{}
	existing := state.ManagedNode{
		Name:       parsed.Name,
		IntentHash: "same-hash",
		Runtime:    "jar",
	}
	res, err := Apply(context.Background(), Options{
		Intent: parsed, Target: ft, Store: store, State: st,
		IntentHash: "same-hash", Existing: &existing,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != "no_change" {
		t.Fatalf("Outcome = %q, want no_change", res.Outcome)
	}
	if len(ft.chmodPaths) != 1 || ft.chmodPaths[0] != "/opt/tron/config.conf" {
		t.Fatalf("Chmod calls = %v, want /opt/tron/config.conf", ft.chmodPaths)
	}
	if ft.chmodPerms[0] != 0o600 {
		t.Errorf("Chmod mode = %#o, want 0600", ft.chmodPerms[0])
	}
}

func TestApply_NoChangeBackfillsStorageRoot(t *testing.T) {
	parsed := minimalIntent()
	parsed.Target.Runtime = "docker"
	parsed.Nodes[0].Storage.Data = "/srv/tron/node0/output-directory"
	store, st := freshStore(t)
	existing := state.ManagedNode{
		Name: parsed.Name, IntentHash: "same-hash", Runtime: "docker",
	}
	res, err := Apply(context.Background(), Options{
		Intent: parsed, Target: &fakeTarget{}, Store: store, State: st,
		IntentHash: "same-hash", Existing: &existing,
		DeploymentsDir: "/srv/tron/deployments",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outcome != "no_change" {
		t.Fatalf("Outcome = %q, want no_change", res.Outcome)
	}
	got := store.GetNode(st, parsed.Name)
	if got == nil || got.StorageRoot != "/srv/tron/node0/output-directory" {
		t.Fatalf("StorageRoot = %v, want /srv/tron/node0/output-directory", got)
	}
}

func TestApply_NoChangeBackfillSaveFailurePropagates(t *testing.T) {
	parsed := minimalIntent()
	parsed.Target.Runtime = "docker"
	parsed.Nodes[0].Storage.Data = "/srv/tron/node0/output-directory"
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	store, err := state.NewStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	st := &state.DeploymentState{}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	// A directory at the state path makes the final atomic rename fail.
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := state.ManagedNode{Name: parsed.Name, IntentHash: "same-hash", Runtime: "docker"}
	res, err := Apply(context.Background(), Options{
		Intent: parsed, Target: &fakeTarget{}, Store: store, State: st,
		IntentHash: "same-hash", Existing: &existing,
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err == nil || res != nil {
		t.Fatalf("Apply = (%v, %v), want propagated save error", res, err)
	}
}

func TestApply_RequiresIntent(t *testing.T) {
	if _, err := Apply(context.Background(), Options{}); err == nil {
		t.Error("expected error when Intent is nil")
	}
}

func TestApply_RequiresIntentHash(t *testing.T) {
	parsed := minimalIntent()
	store, st := freshStore(t)
	if _, err := Apply(context.Background(), Options{
		Intent: parsed,
		Target: &fakeTarget{},
		Store:  store,
		State:  st,
	}); err == nil {
		t.Error("expected error when IntentHash is empty")
	}
}

func TestIntentHashFromBytes_Stable(t *testing.T) {
	// Lower-case hex of SHA256("hello world\n") — sanity check the
	// alias matches sha256 stdlib behavior.
	got := IntentHashFromBytes([]byte("hello world\n"))
	want := "a948904f2f0f479b8f8197694b30184b0d2ed1c1cd2a1ec0fb85d299a192a447"
	if got != want {
		t.Errorf("hash mismatch: got %s, want %s", got, want)
	}
}

// --- helpers ---

func minimalIntent() *intent.Intent {
	return &intent.Intent{
		Name:    "test-node",
		Network: "nile",
		Target:  intent.Target{Type: "local", Runtime: "docker"},
		Nodes: []intent.NodeSpec{{
			Type:      "fullnode",
			Version:   "4.8.1",
			Resources: intent.Resources{Memory: "8GB"},
			Ports:     intent.PortMapping{HTTP: 8090, GRPC: 50051},
		}},
	}
}

func freshStore(t *testing.T) (*state.Store, *state.DeploymentState) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.NewStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return store, st
}

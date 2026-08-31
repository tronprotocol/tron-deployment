package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type bootstrapFakeTarget struct {
	*target.LocalTarget
	provisioning bool
	calls        []string
}

func (t *bootstrapFakeTarget) SetProvisioning(on bool) { t.provisioning = on }

func (t *bootstrapFakeTarget) Exec(_ context.Context, cmd string, args ...string) ([]byte, error) {
	t.calls = append(t.calls, cmd)
	if cmd == "which" && len(args) == 1 && args[0] == "apt-get" {
		return []byte("/usr/bin/apt-get\n"), nil
	}
	return nil, nil
}

func TestBootstrapEnablesProvisioningMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intent.yaml")
	if err := os.WriteFile(path, []byte("name: bootstrap-test\nnetwork: private\ntarget:\n  type: ssh\n  host: example\n  user: deploy\n  runtime: jar\nnodes:\n  - type: fullnode\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &bootstrapFakeTarget{LocalTarget: target.NewLocalTarget()}
	old := bootstrapResolveTarget
	oldPath := bootstrapIntentPath
	bootstrapResolveTarget = func(*intent.Intent) (target.Target, error) { return fake, nil }
	bootstrapIntentPath = path
	defer func() {
		bootstrapResolveTarget = old
		bootstrapIntentPath = oldPath
	}()

	if err := runBootstrap(&cobra.Command{}, nil); err != nil {
		t.Fatal(err)
	}
	if !fake.provisioning {
		t.Fatal("bootstrap did not enable provisioning mode")
	}
	if len(fake.calls) == 0 || fake.calls[0] != "which" {
		t.Fatalf("bootstrap calls = %v, want provisioning probe", fake.calls)
	}
	var sawAPT, sawUseradd bool
	for _, call := range fake.calls {
		sawAPT = sawAPT || call == "apt-get"
		sawUseradd = sawUseradd || call == "useradd"
	}
	if !sawAPT || !sawUseradd {
		t.Fatalf("bootstrap calls = %v, want apt-get and useradd in provisioning mode", fake.calls)
	}
}

// bootstrap has to be re-runnable, so a useradd that fails only because
// the user is already there must not abort the run — but anything else
// must, now that provisioning mode lets the call actually execute.
func TestUserAlreadyExists(t *testing.T) {
	tolerated := []string{
		"useradd: user 'tron' already exists",
		"useradd: UID 999 is not unique\nuseradd: name tron already in use",
		"ALREADY EXISTS",
	}
	for _, out := range tolerated {
		if !userAlreadyExists([]byte(out)) {
			t.Errorf("should tolerate: %q", out)
		}
	}

	fatal := []string{
		"useradd: Permission denied.",
		"useradd: cannot open /etc/passwd",
		"useradd: invalid shell '/usr/sbin/nologin'",
		"",
	}
	for _, out := range fatal {
		if userAlreadyExists([]byte(out)) {
			t.Errorf("should not tolerate: %q", out)
		}
	}
}

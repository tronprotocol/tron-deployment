package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// witnessConfig stands in for the rendered HOCON of a witness node: the
// block-signing key is inlined into the file trond writes (java-tron's
// typesafe-config does no ${ENV} substitution), so the deployment
// directory and the .conf inside it must not be readable by other local
// accounts.
const witnessConfig = `localwitness = [
  "0000000000000000000000000000000000000000000000000000000000000001"
]
`

func TestDockerRuntime_Deploy_ConfigWrittenOwnerOnly(t *testing.T) {
	ft := newFakeTarget()
	rt := NewDockerRuntime(ft, "/tmp/trond-deployments")

	opts := DeployOpts{
		Name:        "my-witness",
		ConfigData:  []byte(witnessConfig),
		ComposeData: []byte("services:\n  my-witness:\n"),
	}
	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	confPath := "/tmp/trond-deployments/my-witness/my-witness.conf"
	if got := string(ft.files[confPath]); got != witnessConfig {
		t.Fatalf("config not written to %s (got %q)", confPath, got)
	}
	perm, ok := ft.perms[confPath]
	if !ok {
		t.Fatalf("no mode recorded for %s", confPath)
	}
	if perm != 0600 {
		t.Errorf("config mode = %#o, want 0600 (secret-bearing config must be owner-only)", perm)
	}
	if perm&0077 != 0 {
		t.Errorf("config mode %#o grants group/other access to the witness key", perm)
	}
}

// The deployment directory must be created restrictive, and restrictive at
// creation time — not widened by the umask and narrowed afterwards by a
// separate chmod, which would both leave a window and re-target a path that
// could have been swapped in between.
func TestDockerRuntime_Deploy_DeployDirCreatedOwnerOnly(t *testing.T) {
	ft := newFakeTarget()
	rt := NewDockerRuntime(ft, "/tmp/trond-deployments")

	opts := DeployOpts{
		Name:        "my-witness",
		ConfigData:  []byte(witnessConfig),
		ComposeData: []byte("services:\n  my-witness:\n"),
	}
	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	if len(ft.cmds) == 0 {
		t.Fatal("no commands executed")
	}
	want := []string{"mkdir", "-p", "-m", "0700", "/tmp/trond-deployments/my-witness"}
	if !reflect.DeepEqual(ft.cmds[0], want) {
		t.Fatalf("first command = %v, want %v", ft.cmds[0], want)
	}
	for _, cmd := range ft.cmds {
		if len(cmd) > 0 && cmd[0] == "chmod" {
			t.Errorf("unexpected post-hoc chmod on a target path: %v", cmd)
		}
	}
}

// dockerStubTarget runs every command on the real local filesystem except
// `docker`, which is recorded and stubbed so the test never talks to a
// container runtime. It lets Deploy exercise the actual mkdir + file write
// and assert the modes that land on disk.
type dockerStubTarget struct {
	*target.LocalTarget
	dockerCalls [][]string
}

func (d *dockerStubTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	if cmd == "docker" {
		d.dockerCalls = append(d.dockerCalls, append([]string{cmd}, args...))
		return nil, nil
	}
	return d.LocalTarget.Exec(ctx, cmd, args...)
}

func newDockerStubTarget() *dockerStubTarget {
	return &dockerStubTarget{LocalTarget: target.NewLocalTarget()}
}

func TestDockerRuntime_Deploy_ModesOnDisk(t *testing.T) {
	ctx := context.Background()
	tgt := newDockerStubTarget()
	if !tgt.CommandExists(ctx, "mkdir") {
		t.Skip("mkdir not available on PATH")
	}

	workDir := t.TempDir()
	rt := NewDockerRuntime(tgt, workDir)

	opts := DeployOpts{
		Name:        "my-witness",
		ConfigData:  []byte(witnessConfig),
		ComposeData: []byte("services:\n  my-witness:\n"),
	}
	if err := rt.Deploy(ctx, opts); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	dir := filepath.Join(workDir, "my-witness")
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat deploy dir: %v", err)
	}
	if di.Mode().Perm() != 0700 {
		t.Errorf("deploy dir mode = %#o, want 0700", di.Mode().Perm())
	}
	if di.Mode().Perm()&0077 != 0 {
		t.Errorf("deploy dir mode %#o is reachable by group/other", di.Mode().Perm())
	}

	confPath := filepath.Join(dir, "my-witness.conf")
	ci, err := os.Stat(confPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if ci.Mode().Perm() != 0600 {
		t.Errorf("config mode = %#o, want 0600", ci.Mode().Perm())
	}

	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != witnessConfig {
		t.Errorf("config content = %q, want %q", string(data), witnessConfig)
	}

	if len(tgt.dockerCalls) != 1 || !strings.Contains(strings.Join(tgt.dockerCalls[0], " "), "up") {
		t.Errorf("expected one `docker compose ... up` call, got %v", tgt.dockerCalls)
	}
}

// Redeploying over an existing deployment directory must still work:
// `mkdir -p -m 0700` is not an error on an existing directory, and the
// rewritten config keeps its restrictive mode.
func TestDockerRuntime_Deploy_RedeployExistingDeployment(t *testing.T) {
	ctx := context.Background()
	tgt := newDockerStubTarget()
	if !tgt.CommandExists(ctx, "mkdir") {
		t.Skip("mkdir not available on PATH")
	}

	workDir := t.TempDir()
	rt := NewDockerRuntime(tgt, workDir)

	opts := DeployOpts{
		Name:        "my-witness",
		ConfigData:  []byte(witnessConfig),
		ComposeData: []byte("services:\n  my-witness:\n"),
	}
	if err := rt.Deploy(ctx, opts); err != nil {
		t.Fatalf("first Deploy failed: %v", err)
	}

	updated := witnessConfig + "node.p2p.version = 11111\n"
	opts.ConfigData = []byte(updated)
	if err := rt.Deploy(ctx, opts); err != nil {
		t.Fatalf("redeploy failed: %v", err)
	}

	dir := filepath.Join(workDir, "my-witness")
	confPath := filepath.Join(dir, "my-witness.conf")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read config after redeploy: %v", err)
	}
	if string(data) != updated {
		t.Errorf("config not updated on redeploy: got %q", string(data))
	}

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat deploy dir: %v", err)
	}
	if di.Mode().Perm()&0077 != 0 {
		t.Errorf("deploy dir mode %#o is reachable by group/other after redeploy", di.Mode().Perm())
	}
	ci, err := os.Stat(confPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if ci.Mode().Perm()&0077 != 0 {
		t.Errorf("config mode %#o is readable by group/other after redeploy", ci.Mode().Perm())
	}

	if len(tgt.dockerCalls) != 2 {
		t.Errorf("expected 2 docker calls across two deploys, got %v", tgt.dockerCalls)
	}
}

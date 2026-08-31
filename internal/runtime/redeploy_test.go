package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/target"
)

// These tests cover one defect with three instances: a deploy writes new
// config to a bind mount or a systemd-invisible path, the orchestrator
// sees no reason to replace the running process, and the command reports
// success while the node keeps serving its old configuration.
//
// The oracle throughout is the command trond actually runs, because that
// is the only thing that determines whether the change reaches the
// process. Asserting that the file was written proves nothing here — the
// broken versions wrote the file too.

// findCmd returns the first recorded invocation of `name`, or nil.
func findCmd(cmds [][]string, name string, mustContain ...string) []string {
	for _, c := range cmds {
		if len(c) == 0 || c[0] != name {
			continue
		}
		ok := true
		for _, want := range mustContain {
			if !slices.Contains(c, want) {
				ok = false
				break
			}
		}
		if ok {
			return c
		}
	}
	return nil
}

func TestDockerRuntime_Deploy_ConfigChangeForcesRecreate(t *testing.T) {
	const compose = "services:\n  n1:\n"
	opts := func(config string) DeployOpts {
		return DeployOpts{Name: "n1", ConfigData: []byte(config), ComposeData: []byte(compose)}
	}

	t.Run("first deploy does not force recreate", func(t *testing.T) {
		ft := newFakeTarget()
		rt := NewDockerRuntime(ft, "/tmp/d")
		if err := rt.Deploy(context.Background(), opts("a = 1\n")); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		up := findCmd(ft.cmds, "docker", "up")
		if up == nil {
			t.Fatal("no `docker compose up` recorded")
		}
		if slices.Contains(up, "--force-recreate") {
			t.Errorf("first deploy used --force-recreate: %v — there is no old "+
				"container to replace, and recreating costs a node restart", up)
		}
	})

	t.Run("unchanged config does not force recreate", func(t *testing.T) {
		ft := newFakeTarget()
		rt := NewDockerRuntime(ft, "/tmp/d")
		ctx := context.Background()
		if err := rt.Deploy(ctx, opts("a = 1\n")); err != nil {
			t.Fatalf("Deploy 1: %v", err)
		}
		ft.cmds = nil
		if err := rt.Deploy(ctx, opts("a = 1\n")); err != nil {
			t.Fatalf("Deploy 2: %v", err)
		}
		up := findCmd(ft.cmds, "docker", "up")
		if up == nil {
			t.Fatal("no `docker compose up` recorded")
		}
		if slices.Contains(up, "--force-recreate") {
			t.Errorf("redeploy of identical config used --force-recreate: %v — "+
				"apply must stay idempotent, a node restart is not free", up)
		}
	})

	t.Run("changed config forces recreate", func(t *testing.T) {
		ft := newFakeTarget()
		rt := NewDockerRuntime(ft, "/tmp/d")
		ctx := context.Background()
		if err := rt.Deploy(ctx, opts("node.rpc.maxConcurrentCallsPerConnection = 5\n")); err != nil {
			t.Fatalf("Deploy 1: %v", err)
		}
		ft.cmds = nil
		// Compose spec is byte-identical; only the bind-mounted config moved.
		if err := rt.Deploy(ctx, opts("node.rpc.maxConcurrentCallsPerConnection = 1\n")); err != nil {
			t.Fatalf("Deploy 2: %v", err)
		}
		up := findCmd(ft.cmds, "docker", "up")
		if up == nil {
			t.Fatal("no `docker compose up` recorded")
		}
		if !slices.Contains(up, "--force-recreate") {
			t.Errorf("config changed but compose was run as %v — without "+
				"--force-recreate the container keeps the config it parsed at "+
				"start and the deploy still reports success", up)
		}
	})
}

func TestJarRuntime_Deploy_RestartsOnlyWhenSomethingChanged(t *testing.T) {
	base := func(config, unit string) DeployOpts {
		return DeployOpts{
			Name:        "n1",
			JarPath:     "/opt/tron/FullNode.jar",
			ConfigData:  []byte(config),
			SystemdData: []byte(unit),
		}
	}
	const unit = "[Service]\nExecStart=/usr/bin/java -jar FullNode.jar\n"

	tests := []struct {
		name          string
		second        DeployOpts
		wantedRestart bool
	}{
		{"identical redeploy", base("a = 1\n", unit), false},
		{"config changed", base("a = 2\n", unit), true},
		{"unit changed", base("a = 1\n", unit+"Environment=X=1\n"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFakeTarget()
			rt := NewJarRuntime(ft)
			ctx := context.Background()
			if err := rt.Deploy(ctx, base("a = 1\n", unit)); err != nil {
				t.Fatalf("Deploy 1: %v", err)
			}
			ft.cmds = nil
			if err := rt.Deploy(ctx, tc.second); err != nil {
				t.Fatalf("Deploy 2: %v", err)
			}
			restart := findCmd(ft.cmds, "systemctl", "restart") != nil
			if restart != tc.wantedRestart {
				if tc.wantedRestart {
					t.Errorf("no `systemctl restart` after the change — "+
						"`enable --now` starts a stopped unit but never restarts a "+
						"running one, so the change never reaches the JVM. Commands: %v",
						ft.cmds)
				} else {
					t.Errorf("restarted on an unchanged redeploy: %v — that turns "+
						"every apply into a node outage", ft.cmds)
				}
			}
		})
	}
}

func TestJarRuntime_Deploy_ConfigAndSecretEnvAreOwnerOnly(t *testing.T) {
	ft := newFakeTarget()
	rt := NewJarRuntime(ft)
	opts := DeployOpts{
		Name:        "n1",
		JarPath:     "/opt/tron/FullNode.jar",
		ConfigData:  []byte(witnessConfig),
		SystemdData: []byte("[Service]\n"),
		EnvVars:     map[string]string{"SR_PRIVATE_KEY": "deadbeef"},
	}
	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	for _, path := range []string{
		"/opt/tron/config.conf",
		"/etc/systemd/system/tron-n1.service.d/env.conf",
	} {
		perm, ok := ft.perms[path]
		if !ok {
			t.Errorf("no mode recorded for %s", path)
			continue
		}
		if perm != 0600 {
			t.Errorf("%s mode = %#o, want 0600", path, perm)
		}
	}
}

type jarDiskTarget struct{ *target.LocalTarget }

func (t *jarDiskTarget) Exec(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	if cmd == "systemctl" {
		return nil, nil
	}
	return t.LocalTarget.Exec(ctx, cmd, args...)
}

func (t *jarDiskTarget) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if strings.HasPrefix(path, "/etc/systemd/") {
		return nil
	}
	return t.LocalTarget.WriteFile(ctx, path, data, perm)
}

func TestJarRuntime_Deploy_TightensExistingConfigOnDisk(t *testing.T) {
	install := t.TempDir()
	configPath := filepath.Join(install, "config.conf")
	if err := os.WriteFile(configPath, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := NewJarRuntime(&jarDiskTarget{LocalTarget: target.NewLocalTarget()})
	opts := DeployOpts{
		Name:        "n1",
		JarPath:     filepath.Join(install, "FullNode.jar"),
		ConfigData:  []byte(witnessConfig),
		SystemdData: []byte("[Service]\n"),
	}
	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
}

// TestJarRuntime_Deploy_EnvVarsAllSurvive covers a separate defect in the
// same function: the drop-in was written once per key inside the loop,
// each write truncating the last, so exactly one variable survived and
// which one depended on Go's randomised map iteration order.
func TestJarRuntime_Deploy_EnvVarsAllSurvive(t *testing.T) {
	ft := newFakeTarget()
	rt := NewJarRuntime(ft)
	opts := DeployOpts{
		Name:        "n1",
		JarPath:     "/opt/tron/FullNode.jar",
		ConfigData:  []byte("a = 1\n"),
		SystemdData: []byte("[Service]\n"),
		EnvVars: map[string]string{
			"SR_PRIVATE_KEY": "deadbeef",
			"TRON_HOME":      "/opt/tron",
			"JAVA_OPTS":      "-Xmx16g",
		},
	}
	if err := rt.Deploy(context.Background(), opts); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	got := string(ft.files["/etc/systemd/system/tron-n1.service.d/env.conf"])
	for k, v := range opts.EnvVars {
		want := "Environment=" + k + "=" + v
		if !strings.Contains(got, want) {
			t.Errorf("drop-in is missing %q — a node can start without its "+
				"witness key depending on map iteration order.\ngot:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "[Service]"); n != 1 {
		t.Errorf("drop-in has %d [Service] sections, want 1:\n%s", n, got)
	}
}

func TestJarRuntime_Deploy_EmptyEnvVarsRemovesOldDropIn(t *testing.T) {
	ft := newFakeTarget()
	rt := NewJarRuntime(ft)
	ctx := context.Background()
	base := DeployOpts{
		Name:        "n1",
		JarPath:     "/opt/tron/FullNode.jar",
		ConfigData:  []byte("a = 1\n"),
		SystemdData: []byte("[Service]\n"),
	}
	withEnv := base
	withEnv.EnvVars = map[string]string{"SR_PRIVATE_KEY": "deadbeef"}
	if err := rt.Deploy(ctx, withEnv); err != nil {
		t.Fatalf("Deploy with env: %v", err)
	}
	ft.cmds = nil
	if err := rt.Deploy(ctx, base); err != nil {
		t.Fatalf("Deploy without env: %v", err)
	}

	envPath := "/etc/systemd/system/tron-n1.service.d/env.conf"
	if cmd := findCmd(ft.cmds, "rm", "-f", envPath); cmd == nil {
		t.Fatalf("old environment drop-in was not removed; commands: %v", ft.cmds)
	}
	if findCmd(ft.cmds, "systemctl", "restart", "tron-n1.service") == nil {
		t.Fatalf("service was not restarted after removing environment drop-in; commands: %v", ft.cmds)
	}
}

func TestJarRuntime_RemoveCleansDropIn(t *testing.T) {
	ft := newFakeTarget()
	rt := NewJarRuntime(ft)
	if err := rt.Remove(context.Background(), "n1", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	overridePath := "/etc/systemd/system/tron-n1.service.d"
	if cmd := findCmd(ft.cmds, "rm", "-rf", overridePath); cmd == nil {
		t.Fatalf("override directory was not removed; commands: %v", ft.cmds)
	}
}

func TestMonitoringRuntime_Deploy_ScrapeConfigChangeForcesRecreate(t *testing.T) {
	opts := func(scrape string) MonitoringDeployOpts {
		return MonitoringDeployOpts{
			Name:              "test",
			ComposeData:       []byte("services:\n  prometheus:\n"),
			PrometheusConfig:  []byte(scrape),
			GrafanaDatasource: []byte("datasources:\n"),
			GrafanaProvider:   []byte("providers:\n"),
		}
	}
	ft := newFakeTarget()
	rt := NewMonitoringRuntime(ft, "/tmp/m")
	ctx := context.Background()
	if err := rt.Deploy(ctx, opts("scrape_configs: [a]\n")); err != nil {
		t.Fatalf("Deploy 1: %v", err)
	}
	ft.cmds = nil
	// Only the bind-mounted scrape config moved; the compose spec did not.
	if err := rt.Deploy(ctx, opts("scrape_configs: [a, b]\n")); err != nil {
		t.Fatalf("Deploy 2: %v", err)
	}
	up := findCmd(ft.cmds, "docker", "up")
	if up == nil {
		t.Fatal("no `docker compose up` recorded")
	}
	if !slices.Contains(up, "--force-recreate") {
		t.Errorf("scrape config changed but compose was run as %v — Prometheus "+
			"would keep scraping the targets it read at start while the new "+
			"config sits on disk", up)
	}
}

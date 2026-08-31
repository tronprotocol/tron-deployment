package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type cleanupFakeTarget struct {
	*target.LocalTarget
	args []string
	err  error
}

func (t *cleanupFakeTarget) Exec(_ context.Context, cmd string, args ...string) ([]byte, error) {
	t.args = append([]string{cmd}, args...)
	return nil, t.err
}

func TestVerifyNodeInvokesVerifyForEachProjectedNode(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "network.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: net
network: private
target:
  type: local
nodes:
  - type: fullnode
    version: latest
  - type: witness
    version: latest
    witness_key_env: SR_KEY
`), 0600); err != nil {
		t.Fatal(err)
	}

	oldRunChild := runChild
	defer func() { runChild = oldRunChild }()
	var intents []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		for i := range argv {
			if argv[i] == "--intent" && i+1 < len(argv) {
				data, err := os.ReadFile(argv[i+1])
				if err != nil {
					t.Fatalf("read projected intent: %v", err)
				}
				intents = append(intents, string(data))
			}
		}
		return nil
	}

	for i, node := range []string{"net-node0", "net-node1"} {
		if err := verifyNode(context.Background(), "trond", node, intentPath, time.Second); err != nil {
			t.Fatalf("verifyNode(%s): %v", node, err)
		}
		wantName := []string{"name: net-node0", "name: net-node1"}[i]
		if !strings.Contains(intents[i], wantName) {
			t.Fatalf("projected intent %q does not target %s", intents[i], node)
		}
	}
	if len(intents) != 2 {
		t.Fatalf("verify calls = %d, want 2", len(intents))
	}
}

func TestVerifyNodeFailureReturnedForFailingNode(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "network.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: net
network: private
target:
  type: local
nodes:
  - type: fullnode
    version: latest
  - type: fullnode
    version: latest
`), 0600); err != nil {
		t.Fatal(err)
	}

	oldRunChild := runChild
	defer func() { runChild = oldRunChild }()
	var calls []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		calls = append(calls, strings.Join(argv, " "))
		if len(calls) == 2 {
			return errors.New("verify node net-node1 failed")
		}
		return nil
	}
	if err := verifyNode(context.Background(), "trond", "net-node0", intentPath, time.Second); err != nil {
		t.Fatal(err)
	}
	err := verifyNode(context.Background(), "trond", "net-node1", intentPath, time.Second)
	if err == nil || !strings.Contains(err.Error(), "verify node net-node1 failed") {
		t.Fatalf("error = %v, want failing node verify error", err)
	}
	if len(calls) != 2 {
		t.Fatalf("verify calls = %d, want 2", len(calls))
	}
}

func TestFinishWithFailureRollsBackNodeWhoseVerifyFailed(t *testing.T) {
	oldAutoRollback := upgradeAutoRollback
	oldRunChild := runChild
	oldResult := runChildResult
	defer func() {
		upgradeAutoRollback = oldAutoRollback
		runChild = oldRunChild
		runChildResult = oldResult
	}()
	upgradeAutoRollback = true
	var rollbackNodes []string
	runChildResult = func(_ context.Context, _ string, argv ...string) (error, bool) {
		if len(argv) >= 2 && argv[0] == "rollback" {
			rollbackNodes = append(rollbackNodes, argv[1])
		}
		return nil, false
	}

	err := finishWithFailure(context.Background(), "", "net", "trond", nil,
		[]upgradeStep{{Node: "net-node0", Phase: "upgrade", Status: "ok"},
			{Node: "net-node0", Phase: "verify", Status: "failed"}},
		[]string{"net-node0"}, time.Now(), "net-node0", errors.New("verify failed"))
	if err == nil || !strings.Contains(err.Error(), "net-node0") {
		t.Fatalf("error = %v, want failed node", err)
	}
	if len(rollbackNodes) != 1 || rollbackNodes[0] != "net-node0" {
		t.Fatalf("rollback nodes = %v, want [net-node0]", rollbackNodes)
	}
}

func TestRunChildCommandRejectsNonJSONOutput(t *testing.T) {
	err := runChildCommand(context.Background(), "/bin/sh", "-c", "printf not-json")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v, want invalid JSON", err)
	}
}

func TestRunChildCommandRejectsNonObjectJSON(t *testing.T) {
	for _, value := range []string{"null", "1", "[]"} {
		t.Run(value, func(t *testing.T) {
			err := runChildCommand(context.Background(), "/bin/sh", "-c", "printf '%s' '"+value+"'")
			if err == nil || !strings.Contains(err.Error(), "not an object") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunChildCommandMarksTruncatedStderr(t *testing.T) {
	err := runChildCommand(context.Background(), "python3", "-c", "import sys; sys.stderr.write('x'*1100001); sys.exit(1)")
	if err == nil || !strings.Contains(err.Error(), "[truncated at 1MiB]") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunChildCommandResultReadsArtifactSwappedFromErrorEnvelope(t *testing.T) {
	err, swapped := runChildCommandWithResult(context.Background(), "/bin/sh", "-c",
		`printf 'Warning: telemetry unavailable\n{"error_code":"UPGRADE_ERROR","message":"state failed","suggestions":["retry"],"artifact_swapped":true}' >&2; exit 1`)
	if err == nil || !strings.Contains(err.Error(), "child command failed") {
		t.Fatalf("error = %v, want child command failure", err)
	}
	if !swapped {
		t.Fatal("artifact_swapped = false, want true")
	}
}

func TestRunChildCommandResultDoesNotMarkPreActivateFailure(t *testing.T) {
	err, swapped := runChildCommandWithResult(context.Background(), "/bin/sh", "-c",
		`printf '{"error_code":"UPGRADE_ERROR","message":"prepare failed"}' >&2; exit 1`)
	if err == nil {
		t.Fatal("expected child command failure")
	}
	if swapped {
		t.Fatal("artifact_swapped = true, want false")
	}
}

func TestRunNetworkChildResultSurfacesArtifactSwapped(t *testing.T) {
	old := runChildWithEnvResult
	defer func() { runChildWithEnvResult = old }()
	runChildWithEnvResult = func(context.Context, string, []string, ...string) (error, bool) {
		return errors.New("post-activate failure"), true
	}

	err, swapped := runNetworkChildResult(context.Background(), "trond",
		&state.DeploymentState{Nodes: []state.ManagedNode{{Name: "net-node0", Runtime: "jar"}}},
		"net-node0", "upgrade", "net-node0")
	if err == nil || !swapped {
		t.Fatalf("result = (%v, %v), want failure with artifact_swapped=true", err, swapped)
	}
}

func TestRollbackChildEnvUsesPreAttemptMetadata(t *testing.T) {
	tests := []struct {
		name, version, previous string
		want                    []string
	}{
		{"prior upgrade", "A", "A0", []string{networkUpgradeRestoreEnv, networkUpgradeTargetEnv + "=A", networkUpgradePreviousEnv + "=A0"}},
		{"first upgrade", "A", "", []string{networkUpgradeRestoreEnv, networkUpgradeTargetEnv + "=A", networkUpgradePreviousEnv + "="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rollbackChildEnv(&state.ManagedNode{Version: tt.version, PreviousVersion: tt.previous})
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("env = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunChildCommandWithEnvResultStripsInheritedUpgradeMetadata(t *testing.T) {
	t.Setenv(networkUpgradeRestoreEnv[:len(networkUpgradeRestoreEnv)-2], "0")
	t.Setenv("TROND_PRESERVE_BACKUP", "0")
	t.Setenv(networkUpgradeTargetEnv, "stale-target")
	t.Setenv(networkUpgradePreviousEnv, "stale-previous")
	env := childEnv([]string{networkUpgradeTargetEnv + "=A", networkUpgradePreviousEnv + "=A0"})
	values := map[string]string{}
	for _, entry := range env {
		if strings.HasPrefix(entry, "TROND_NETWORK_UPGRADE") || strings.HasPrefix(entry, "TROND_PRESERVE_BACKUP") {
			parts := strings.SplitN(entry, "=", 2)
			values[parts[0]] = parts[1]
		}
	}
	if values[networkUpgradeTargetEnv] != "A" || values[networkUpgradePreviousEnv] != "A0" ||
		values["TROND_NETWORK_UPGRADE"] != "" || values["TROND_PRESERVE_BACKUP"] != "" {
		t.Fatalf("upgrade env values = %v, want target=A previous=A0", values)
	}
}

func TestRunUpgradeIncludesSwappedFailureWithPreAttemptRollbackMetadata(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: []state.ManagedNode{{
		Name: "net-node0", Runtime: "jar", Network: "private", Version: "A", PreviousVersion: "A0",
	}}}); err != nil {
		t.Fatal(err)
	}

	oldFn, oldAuto, oldVersion := runNetworkChildResultFn, upgradeAutoRollback, upgradeVersion
	defer func() { runNetworkChildResultFn, upgradeAutoRollback, upgradeVersion = oldFn, oldAuto, oldVersion }()
	upgradeAutoRollback, upgradeVersion = true, "B"
	var rollbackEnv []string
	oldEnvFn := runChildWithEnvResult
	defer func() { runChildWithEnvResult = oldEnvFn }()
	runChildWithEnvResult = func(_ context.Context, _ string, env []string, argv ...string) (error, bool) {
		if argv[0] == "rollback" {
			rollbackEnv = append(append([]string(nil), env...), argv...)
		}
		return nil, false
	}
	runNetworkChildResultFn = func(ctx context.Context, exe string, st *state.DeploymentState, node string, argv ...string) (error, bool) {
		if argv[0] == "rollback" {
			return runNetworkChildResult(ctx, exe, st, node, argv...)
		}
		return errors.New("swapped child failure"), true
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("output", "", "")
	err = runUpgrade(cmd, []string{"net"})
	if err == nil || !strings.Contains(err.Error(), "net-node0") {
		t.Fatalf("error = %v", err)
	}
	if len(rollbackEnv) < 3 || rollbackEnv[1] != networkUpgradeTargetEnv+"=A" || rollbackEnv[2] != networkUpgradePreviousEnv+"=A0" {
		t.Fatalf("rollback env = %v, want pre-attempt A/A0", rollbackEnv)
	}
}

func TestLimitedBufferTracksOverflow(t *testing.T) {
	var b limitedBuffer
	_, _ = b.Write(make([]byte, (1<<20)+1))
	if !b.limited || b.total <= 1<<20 || b.Len() != 1<<20 {
		t.Fatalf("buffer state limited=%v total=%d len=%d", b.limited, b.total, b.Len())
	}
}

func TestFinishWithFailureDoesNotPassUpgradeJarArgumentsToRollback(t *testing.T) {
	oldAuto, oldURL, oldSHA, oldRun, oldResult := upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256, runChild, runChildResult
	defer func() {
		upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256, runChild, runChildResult = oldAuto, oldURL, oldSHA, oldRun, oldResult
	}()
	upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256 = true, "https://example/jar", "abc"
	var got []string
	runChildResult = func(_ context.Context, _ string, argv ...string) (error, bool) {
		got = append([]string(nil), argv...)
		return nil, false
	}
	_ = finishWithFailure(context.Background(), "", "net", "trond", nil, nil, []string{"net-node0"}, time.Now(), "net-node1", errors.New("failed"))
	want := []string{"rollback", "net-node0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback args = %v, want %v", got, want)
	}
}

func TestRunNetworkChildOnlyInjectsUpgradeEnvForRollback(t *testing.T) {
	st := &state.DeploymentState{Nodes: []state.ManagedNode{{Name: "net-node0", Runtime: "jar"}}}
	old := runChildWithEnvResult
	oldChild := runChild
	defer func() { runChildWithEnvResult = old; runChild = oldChild }()
	var envCalls [][]string
	runChildWithEnvResult = func(_ context.Context, _ string, env []string, _ ...string) (error, bool) {
		envCalls = append(envCalls, append([]string(nil), env...))
		return nil, false
	}
	runChild = func(context.Context, string, ...string) error { return nil }
	if err := runNetworkChild(context.Background(), "trond", st, "net-node0", "upgrade", "net-node0"); err != nil {
		t.Fatal(err)
	}
	if len(envCalls) != 1 || !reflect.DeepEqual(envCalls[0], []string{networkUpgradePreserveEnv}) {
		t.Fatalf("upgrade env = %v, want [%s]", envCalls, networkUpgradePreserveEnv)
	}
	if err := runNetworkChild(context.Background(), "trond", st, "net-node0", "rollback", "net-node0"); err != nil {
		t.Fatal(err)
	}
	if len(envCalls) != 2 || !reflect.DeepEqual(envCalls[1], []string{networkUpgradeRestoreEnv, networkUpgradeTargetEnv + "=", networkUpgradePreviousEnv + "="}) {
		t.Fatalf("rollback env = %v, want [%s %s= %s=]", envCalls, networkUpgradeRestoreEnv, networkUpgradeTargetEnv, networkUpgradePreviousEnv)
	}
}

func TestCleanupNetworkBackupUsesRmArgv(t *testing.T) {
	fake := &cleanupFakeTarget{LocalTarget: target.NewLocalTarget()}
	old := fromManagedNode
	fromManagedNode = func(*state.ManagedNode) (target.Target, error) { return fake, nil }
	defer func() { fromManagedNode = old }()

	st := &state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "net-node0", Runtime: "jar", InstallPath: "/srv/tron path",
	}}}
	if err := cleanupNetworkBackup(context.Background(), st, "net-node0"); err != nil {
		t.Fatal(err)
	}
	want := []string{"rm", "-f", "--", "/srv/tron path/FullNode.jar.upgrade.backup"}
	if !reflect.DeepEqual(fake.args, want) {
		t.Fatalf("cleanup argv = %q, want %q", fake.args, want)
	}
}

func TestCleanupNetworkBackupsReturnsWarningWithoutUpgradeFailure(t *testing.T) {
	fake := &cleanupFakeTarget{LocalTarget: target.NewLocalTarget(), err: errors.New("rm failed")}
	old := fromManagedNode
	fromManagedNode = func(*state.ManagedNode) (target.Target, error) { return fake, nil }
	defer func() { fromManagedNode = old }()

	warnings := cleanupNetworkBackups(context.Background(), &state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "net-node0", Runtime: "jar", InstallPath: "/srv/tron",
	}}}, []string{"net-node0"})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "net-node0") || !strings.Contains(warnings[0], "rm failed") {
		t.Fatalf("warnings = %v, want cleanup warning", warnings)
	}
}

package network

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

func TestCountFailedDeployments(t *testing.T) {
	nodes := []map[string]any{
		{"name": "net-node0", "status": "running"},
		{"name": "net-node1", "status": "error"},
		{"name": "net-node2", "status": "error"},
	}
	if got := countFailedDeployments(nodes); got != 2 {
		t.Fatalf("failed deployments = %d, want 2", got)
	}
}

func TestRunCreatePartialFailureContract(t *testing.T) {
	oldApply, oldTarget, oldMonitoring := createApply, createTarget, createMonitoring
	defer func() { createApply, createTarget, createMonitoring = oldApply, oldTarget, oldMonitoring }()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	intentPath := filepath.Join(t.TempDir(), "network.yaml")
	if err := os.WriteFile(intentPath, []byte("name: net\nnetwork: private\ntarget:\n  type: local\nnodes:\n  - type: fullnode\n  - type: fullnode\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPath, oldMonitor := createIntentPath, createMonitor
	createIntentPath, createMonitor = intentPath, true
	t.Cleanup(func() { createIntentPath, createMonitor = oldPath, oldMonitor })
	fake := &createFakeTarget{LocalTarget: target.NewLocalTarget()}
	createTarget = func(*intent.Intent) (target.Target, error) { return fake, nil }
	var calls int
	createApply = func(_ context.Context, opts apply.Options) (*apply.Result, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("node failure")
		}
		return &apply.Result{Name: opts.Intent.Name, Outcome: "created"}, nil
	}
	monitorCalled := false
	createMonitoring = func(context.Context, target.Target, string, *intent.Intent) monitoringResult {
		monitorCalled = true
		return monitoringResult{}
	}
	cmd := newNetCmd()
	oldOut := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	gotErr := runCreate(cmd, nil)
	_ = w.Close()
	os.Stdout = oldOut
	body, _ := io.ReadAll(r)
	var structured *output.StructuredError
	if gotErr == nil || !errors.As(gotErr, &structured) || structured.Code != "DEPLOY_ERROR" || structured.ExitCode == 0 {
		t.Fatalf("error = %v", gotErr)
	}
	if !strings.Contains(string(body), `"status": "failed"`) || !strings.Contains(string(body), "net-node0") || !strings.Contains(string(body), "net-node1") {
		t.Fatalf("output = %s", body)
	}
	if monitorCalled {
		t.Fatal("monitoring started after partial node failure")
	}
}

type createFakeTarget struct{ *target.LocalTarget }

func (*createFakeTarget) Exec(context.Context, string, ...string) ([]byte, error) { return nil, nil }

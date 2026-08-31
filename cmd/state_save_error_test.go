package cmd

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	runtimepkg "github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type startTestRuntime struct{}

func (startTestRuntime) Deploy(context.Context, runtimepkg.DeployOpts) error { return nil }
func (startTestRuntime) Start(context.Context, string) error                 { return nil }
func (startTestRuntime) Stop(context.Context, string) error                  { return nil }
func (startTestRuntime) Remove(context.Context, string, bool) error          { return nil }
func (startTestRuntime) Status(context.Context, string) (*runtimepkg.NodeStatus, error) {
	return nil, nil
}
func (startTestRuntime) Logs(context.Context, string, runtimepkg.LogOpts) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

// TestPersistStateSaveFailuresReturnStateErrorAndAudit covers the six
// persistence handlers directly. The start command's resolver wiring is
// covered separately by TestRunStart_StateSaveFailure.
func TestPersistStateSaveFailuresReturnStateErrorAndAudit(t *testing.T) {
	cases := []struct {
		name string
		set  func(func(*nodeContext) error) func(*nodeContext) error
		call func(string, *nodeContext, time.Time) error
	}{
		{"start", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveStartState
			saveStartState = v
			return old
		}, persistStartState},
		{"stop", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveStopState
			saveStopState = v
			return old
		}, persistStopState},
		{"restart", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveRestartState
			saveRestartState = v
			return old
		}, persistRestartState},
		{"remove", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveRemoveState
			saveRemoveState = v
			return old
		}, persistRemoveState},
		{"rollback", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveRollbackState
			saveRollbackState = v
			return old
		}, persistRollbackState},
		{"upgrade", func(v func(*nodeContext) error) func(*nodeContext) error {
			old := saveUpgradeState
			saveUpgradeState = v
			return old
		}, persistUpgradeState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths.SetBaseDir(t.TempDir())
			t.Cleanup(func() { paths.SetBaseDir("") })
			old := tc.set(func(*nodeContext) error { return errors.New("injected save failure") })
			t.Cleanup(func() { tc.set(old) })
			nc := &nodeContext{Node: &state.ManagedNode{Name: "n0"}, Target: target.NewLocalTarget()}
			err := tc.call("n0", nc, time.Now())
			structured, ok := err.(*output.StructuredError)
			if !ok || structured.Code != "STATE_ERROR" {
				t.Fatalf("error = %v, want STATE_ERROR", err)
			}
			entries := auditLines(t)
			if len(entries) != 1 || entries[0][0] != tc.name || entries[0][2] != "error" {
				t.Fatalf("audit = %+v, want %s/error", entries, tc.name)
			}
		})
	}
}

func TestRunStart_StateSaveFailure(t *testing.T) {
	oldResolve, oldSave := resolveStartNodeContext, saveStartState
	t.Cleanup(func() { resolveStartNodeContext, saveStartState = oldResolve, oldSave })
	nc := &nodeContext{
		Node:    &state.ManagedNode{Name: "n0", Status: "stopped"},
		Target:  target.NewLocalTarget(),
		Runtime: startTestRuntime{},
	}
	resolveStartNodeContext = func(string) (*nodeContext, error) { return nc, nil }
	saveStartState = func(*nodeContext) error { return errors.New("injected save failure") }

	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	cmd := newCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := runStart(cmd, []string{"n0"}); err == nil {
		t.Fatal("runStart succeeded after state save failure")
	} else if structured, ok := err.(*output.StructuredError); !ok || structured.Code != "STATE_ERROR" {
		t.Fatalf("runStart error = %v, want STATE_ERROR", err)
	}
	entries := auditLines(t)
	if len(entries) != 1 || entries[0][0] != "start" || entries[0][2] != "error" {
		t.Fatalf("audit = %+v, want start/error", entries)
	}
}

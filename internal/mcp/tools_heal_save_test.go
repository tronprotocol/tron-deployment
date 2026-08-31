package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

func TestAutoHealTool_StateSaveFailureMarksHealedFailedAndStillFailing(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	port := closedHealPort(t)
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "heal0", Runtime: "docker", Status: "stopped", Network: "private", HTTPPort: port,
	}}}); err != nil {
		t.Fatal(err)
	}

	originalSave, originalRun := saveHealState, runHealAction
	t.Cleanup(func() { saveHealState, runHealAction = originalSave, originalRun })
	saveHealState = func(*state.Store, *state.DeploymentState) error {
		return errors.New("injected state save failure")
	}
	runHealAction = func(context.Context, target.Target, *state.ManagedNode, mcpHealAction) error {
		return nil
	}

	res, _, err := autoHealTool(context.Background(), nil, autoHealArgs{Name: "heal0"})
	if err != nil {
		t.Fatalf("autoHealTool: %v", err)
	}
	body := extractText(t, res)
	var got struct {
		Healed []struct {
			Result string `json:"result"`
		} `json:"healed"`
		StillFailing []struct {
			Name string `json:"name"`
		} `json:"still_failing"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode auto_heal result: %v", err)
	}
	if len(got.Healed) != 1 || got.Healed[0].Result != "failed" {
		t.Fatalf("healed = %+v, want one failed entry", got.Healed)
	}
	found := false
	for _, check := range got.StillFailing {
		if check.Name == "port_listening" {
			found = true
		}
	}
	if !found {
		t.Fatalf("still_failing = %+v, want port_listening", got.StillFailing)
	}
}

func closedHealPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

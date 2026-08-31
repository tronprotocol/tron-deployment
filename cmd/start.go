package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var startCmd = &cobra.Command{
	Use:   "start <node>",
	Short: "Start a stopped node",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

// saveStartState is a test injection seam; production default persists via nodeContext.SaveState.
var saveStartState = func(nc *nodeContext) error { return nc.SaveState() }

// resolveStartNodeContext is a test injection seam; production default keeps
// the state write lock until the caller has persisted its mutation.
var resolveStartNodeContext = resolveNodeContextForWrite

func init() {
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	nc, err := resolveStartNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "start", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "START_ERROR", Start: start})
		return exitWithError("START_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to start %s: %v", name, err))
	}

	nc.Node.Status = "running"
	if err := persistNodeState("start", name, nc, start, saveStartState); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "start", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{"name": name, "status": "running"})
	return nil
}

func persistNodeState(command, name string, nc *nodeContext, start time.Time, save func(*nodeContext) error) error {
	if err := save(nc); err != nil {
		writeAudit(auditEvent{Command: command, Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "STATE_ERROR", Start: start})
		return exitWithError("STATE_ERROR", output.ExitGeneralError, fmt.Sprintf("Failed to persist %s state: %v", name, err))
	}
	return nil
}

// Compatibility wrappers retain the existing test seams while sharing the implementation.
func persistStartState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("start", name, nc, start, saveStartState)
}
func persistStopState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("stop", name, nc, start, saveStopState)
}
func persistRestartState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("restart", name, nc, start, saveRestartState)
}
func persistRemoveState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("remove", name, nc, start, saveRemoveState)
}
func persistRollbackState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("rollback", name, nc, start, saveRollbackState)
}
func persistUpgradeState(name string, nc *nodeContext, start time.Time) error {
	return persistNodeState("upgrade", name, nc, start, saveUpgradeState)
}

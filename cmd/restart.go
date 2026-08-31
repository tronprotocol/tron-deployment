package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var restartCmd = &cobra.Command{
	Use:   "restart <node>",
	Short: "Restart a node (stop + start)",
	Args:  cobra.ExactArgs(1),
	RunE:  runRestart,
}

// saveRestartState is a test injection seam; production default persists via nodeContext.SaveState.
var saveRestartState = func(nc *nodeContext) error { return nc.SaveState() }

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	nc, err := resolveNodeContextForWrite(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "RESTART_ERROR", Start: start})
		return exitWithError("RESTART_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to stop %s: %v", name, err))
	}

	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		nc.Node.Status = "error"
		if saveErr := persistRestartState(name, nc, start); saveErr != nil {
			return saveErr
		}
		writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "RESTART_ERROR", Start: start})
		return exitWithError("RESTART_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to start %s after stop: %v", name, err))
	}

	nc.Node.Status = "running"
	if err := persistNodeState("restart", name, nc, start, saveRestartState); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{"name": name, "status": "running"})
	return nil
}

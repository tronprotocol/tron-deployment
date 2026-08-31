package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var stopCmd = &cobra.Command{
	Use:   "stop <node>",
	Short: "Stop a running node",
	Args:  cobra.ExactArgs(1),
	RunE:  runStop,
}

// saveStopState is a test injection seam; production default persists via nodeContext.SaveState.
var saveStopState = func(nc *nodeContext) error { return nc.SaveState() }

func init() {
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
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
		writeAudit(auditEvent{Command: "stop", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "STOP_ERROR", Start: start})
		return exitWithError("STOP_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to stop %s: %v", name, err))
	}

	nc.Node.Status = "stopped"
	if err := persistNodeState("stop", name, nc, start, saveStopState); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "stop", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{"name": name, "status": "stopped"})
	return nil
}

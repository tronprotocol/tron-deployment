package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var (
	removeKeepData bool
	removeConfirm  string
)

var removeCmd = &cobra.Command{
	Use:   "remove <node>",
	Short: "Remove a deployed node",
	Long:  "Stop and remove a deployed node. Use --confirm <name> to confirm destructive removal.",
	Args:  cobra.ExactArgs(1),
	RunE:  runRemove,
}

func init() {
	removeCmd.Flags().BoolVar(&removeKeepData, "keep-data", false, "Keep data volumes/directories")
	removeCmd.Flags().StringVar(&removeConfirm, "confirm", "", "Confirm removal by repeating the node name")
	rootCmd.AddCommand(removeCmd)
}

func runRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	// --require-private precedes the destructive-confirm gate: an agent must
	// learn "not private" (PRIVATE_NETWORK_REQUIRED, exit 2) before it is
	// asked to confirm a destroy (HUMAN_REQUIRED, exit 10). State-only check,
	// no target resolution.
	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	// Require confirmation
	if removeConfirm != name {
		return exitWithError("HUMAN_REQUIRED", output.ExitHumanRequired,
			fmt.Sprintf("Destructive operation: removing node %q", name),
			fmt.Sprintf("Confirm with: trond remove %s --confirm %s", name, name))
	}

	start := time.Now()
	nc, err := resolveNodeContextForWrite(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Remove monitoring stack if present (best-effort, before node removal).
	if nc.Node.Monitoring != nil && nc.Node.Monitoring.Enabled {
		// Determine where monitoring was deployed.
		monTarget := nc.Target
		if nc.Node.Monitoring.TargetType == "ssh" {
			// Jar + SSH: monitoring ran on the trond machine.
			monTarget = target.NewLocalTarget()
		}
		monRT := runtime.NewMonitoringRuntime(monTarget, deploymentsDir())
		if err := monRT.Remove(cmd.Context(), name, true); err != nil {
			// Non-fatal: log but continue with node removal.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove monitoring stack: %v\n", err)
		}
	}

	purge := !removeKeepData
	if err := nc.Runtime.Remove(cmd.Context(), name, purge); err != nil {
		writeAudit(auditEvent{Command: "remove", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "REMOVE_ERROR", Start: start})
		return exitWithError("REMOVE_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to remove %s: %v", name, err))
	}

	nc.Store.RemoveNode(nc.State, name)
	nc.Store.Save(nc.State)
	writeAudit(auditEvent{Command: "remove", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{
		"name":      name,
		"status":    "removed",
		"keep_data": removeKeepData,
	})
	return nil
}

package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var rollbackCmd = &cobra.Command{
	Use:   "rollback <node>",
	Short: "Rollback a node to its previous version",
	Args:  cobra.ExactArgs(1),
	RunE:  runRollback,
}

func init() {
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
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

	if nc.Node.PreviousVersion == "" {
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError,
			fmt.Sprintf("No previous version recorded for %s", name),
			"Rollback is only available after an upgrade")
	}

	currentVersion := nc.Node.Version
	targetVersion := nc.Node.PreviousVersion

	// Stop current. A stop failure is surfaced as a warning rather than
	// a hard error — the node may already be down (which is the common
	// case when the operator runs rollback after a crash), but a
	// genuinely stuck process should not be hidden either.
	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: stop %s before rollback failed: %v\n", name, err)
	}

	// Restore previous version
	nc.Node.Version = targetVersion
	nc.Node.PreviousVersion = currentVersion

	// Start
	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Rollback to %s failed: %v", targetVersion, err))
	}

	nc.Node.Status = "running"
	nc.SaveState()
	writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{
		"name":             name,
		"status":           "running",
		"version":          targetVersion,
		"rolled_back_from": currentVersion,
	})
	return nil
}

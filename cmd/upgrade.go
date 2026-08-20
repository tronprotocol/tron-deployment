package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var upgradeVersion string

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <node>",
	Short: "Upgrade a running node to a new version",
	Long: `Safely upgrade a node: download new jar/pull new image → stop → replace → start → verify.
On failure, automatically rolls back to the previous version.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

func init() {
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "Target version (required)")
	mustMarkRequired(upgradeCmd, "version")
	rootCmd.AddCommand(upgradeCmd)
}

func runUpgrade(cmd *cobra.Command, args []string) error {
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

	previousVersion := nc.Node.Version

	// Stop the node
	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "UPGRADE_ERROR", Start: start})
		return exitWithError("UPGRADE_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to stop %s for upgrade: %v", name, err))
	}

	// Update version in state
	nc.Node.PreviousVersion = previousVersion
	nc.Node.Version = upgradeVersion

	// Start with new version. On failure, restore the previous version
	// in state and try to bring it back up. Surface BOTH errors to the
	// user — a silent rollback-failed leaves the operator thinking the
	// node is back when in fact nothing is running.
	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		nc.Node.Version = previousVersion
		nc.Node.PreviousVersion = ""
		rollbackErr := nc.Runtime.Start(cmd.Context(), name)
		nc.SaveState()
		writeAudit(auditEvent{
			Command:   "upgrade",
			Node:      name,
			Target:    nc.Target.String(),
			Result:    "rollback",
			ErrorCode: "UPGRADE_ERROR",
			Start:     start,
		})

		msg := fmt.Sprintf("Upgrade failed, rolled back to %s: %v", previousVersion, err)
		if rollbackErr != nil {
			msg = fmt.Sprintf("%s; rollback start ALSO failed: %v", msg, rollbackErr)
		}
		return exitWithError("UPGRADE_ERROR", output.ExitGeneralError,
			msg,
			"Check logs: trond logs "+name,
			"Run diagnostics: trond diagnose "+name)
	}

	nc.Node.Status = "running"
	nc.SaveState()
	writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{
		"name":             name,
		"status":           "running",
		"version":          upgradeVersion,
		"previous_version": previousVersion,
	})
	return nil
}

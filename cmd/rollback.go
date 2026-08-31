package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
)

var rollbackJarURL string
var rollbackJarSHA256 string

var rollbackCmd = &cobra.Command{
	Use:   "rollback <node>",
	Short: "Rollback a node to its previous version",
	Args:  cobra.ExactArgs(1),
	RunE:  runRollback,
}

// saveRollbackState is a test injection seam; production default persists via nodeContext.SaveState.
var saveRollbackState = func(nc *nodeContext) error { return nc.SaveState() }

var resolveRollbackNodeContext = resolveNodeContextForWrite

func init() {
	rollbackCmd.Flags().StringVar(&rollbackJarURL, "jar-url", "", "Target-version JAR URL (jar runtime)")
	rollbackCmd.Flags().StringVar(&rollbackJarSHA256, "jar-sha256", "", "Optional target-version JAR SHA256 (jar runtime)")
	rootCmd.AddCommand(rollbackCmd)
}

func runRollback(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	nc, err := resolveRollbackNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	networkRestore := os.Getenv("TROND_NETWORK_UPGRADE") == "1"
	if nc.Node.PreviousVersion == "" && !networkRestore {
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError,
			fmt.Sprintf("No previous version recorded for %s", name),
			"Rollback is only available after an upgrade")
	}

	currentVersion := nc.Node.Version
	targetVersion := nc.Node.PreviousVersion
	if networkRestore {
		if value := os.Getenv("TROND_NETWORK_UPGRADE_TARGET_VERSION"); value != "" {
			targetVersion = value
		} else if targetVersion == "" {
			targetVersion = currentVersion
		}
	}
	preUpgradePreviousVersion, hasPreUpgradePreviousVersion := os.LookupEnv("TROND_NETWORK_UPGRADE_PREVIOUS_VERSION")

	upgrader, supported := nc.Runtime.(runtime.ArtifactUpgrader)
	if !supported {
		writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError, fmt.Sprintf("Runtime for %s does not support safe artifact rollbacks", name))
	}
	tx, err := upgrader.PrepareArtifact(cmd.Context(), name, runtime.UpgradeOpts{Version: targetVersion, JarURL: rollbackJarURL, JarSHA256: rollbackJarSHA256})
	if err != nil {
		writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError, fmt.Sprintf("Prepare rollback failed: %v", err))
	}
	// Stop failures are hard errors: do not activate a replacement artifact
	// while the old process may still be running, or two artifacts could race
	// for the same node resources.
	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		_ = tx.Cleanup(cmd.Context())
		writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
		return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError, fmt.Sprintf("Failed to stop %s: %v", name, err))
	}
	if err := tx.Activate(cmd.Context()); err != nil {
		return rollbackArtifact(cmd, nc, tx, name, currentVersion, start, fmt.Sprintf("activate artifact: %v", err))
	}

	// Start
	if err := tx.Start(cmd.Context()); err != nil {
		return rollbackArtifact(cmd, nc, tx, name, currentVersion, start, fmt.Sprintf("start artifact: %v", err))
	}
	if nc.Node.Runtime == "jar" {
		if err := refreshArtifactSHA256(cmd.Context(), nc); err != nil {
			// The replacement artifact is running while state still records the
			// old version/digest; leave the skew visible until a later success.
			writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
			return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError, fmt.Sprintf("hash rollback artifact: %v", err))
		}
	}
	if err := tx.Cleanup(cmd.Context()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cleanup rollback artifact: %v\n", err)
	}

	// Commit state only after the replacement artifact is running.
	nc.Node.Version = targetVersion
	if networkRestore && hasPreUpgradePreviousVersion {
		nc.Node.PreviousVersion = preUpgradePreviousVersion
	} else {
		nc.Node.PreviousVersion = currentVersion
	}
	nc.Node.Status = "running"
	if err := persistNodeState("rollback", name, nc, start, saveRollbackState); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{
		"name":             name,
		"status":           "running",
		"version":          targetVersion,
		"rolled_back_from": currentVersion,
	})
	return nil
}

func rollbackArtifact(cmd *cobra.Command, nc *nodeContext, tx runtime.ArtifactTransaction, name, previousVersion string, start time.Time, cause string) error {
	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		cause = fmt.Sprintf("%s; stop failed: %v", cause, err)
	}
	rollbackErr := tx.Rollback(cmd.Context())
	if rollbackErr == nil {
		rollbackErr = tx.Start(cmd.Context())
	}
	if rollbackErr == nil {
		if err := tx.Cleanup(cmd.Context()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cleanup rollback artifact: %v\n", err)
		}
	}
	writeAudit(auditEvent{Command: "rollback", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "ROLLBACK_ERROR", Start: start})
	msg := fmt.Sprintf("Rollback failed, restored %s: %s", previousVersion, cause)
	if rollbackErr != nil {
		msg = fmt.Sprintf("%s; restore ALSO failed: %v", msg, rollbackErr)
	}
	return exitWithError("ROLLBACK_ERROR", output.ExitGeneralError, msg)
}

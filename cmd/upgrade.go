package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
)

var upgradeVersion string
var upgradeJarURL string
var upgradeJarSHA256 string

var upgradeCmd = &cobra.Command{
	Use:   "upgrade <node>",
	Short: "Upgrade a running node to a new version",
	Long: `Safely upgrade a node: download new jar/pull new image → stop → replace → start → verify.
On failure, automatically rolls back to the previous version.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

// saveUpgradeState is a test injection seam; production default persists via nodeContext.SaveState.
var saveUpgradeState = func(nc *nodeContext) error { return nc.SaveState() }

func init() {
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "", "Target version (required)")
	upgradeCmd.Flags().StringVar(&upgradeJarURL, "jar-url", "", "Target-version JAR URL (jar runtime)")
	upgradeCmd.Flags().StringVar(&upgradeJarSHA256, "jar-sha256", "", "Optional target-version JAR SHA256 (jar runtime)")
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

	upgrader, supported := nc.Runtime.(runtime.ArtifactUpgrader)
	if !supported {
		writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "UPGRADE_ERROR", Start: start})
		return exitWithError("UPGRADE_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Runtime for %s does not support safe artifact upgrades", name),
			"Use a Docker or JAR runtime")
	}
	// Prepare (pull/download and verify) while the node is still running.
	tx, err := upgrader.PrepareArtifact(cmd.Context(), name, runtime.UpgradeOpts{Version: upgradeVersion, JarURL: upgradeJarURL, JarSHA256: upgradeJarSHA256})
	if err != nil {
		writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "UPGRADE_ERROR", Start: start})
		return exitWithError("UPGRADE_ERROR", output.ExitGeneralError, fmt.Sprintf("Prepare upgrade failed: %v", err))
	}

	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		_ = tx.Cleanup(cmd.Context())
		writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "UPGRADE_ERROR", Start: start})
		return exitWithError("UPGRADE_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to stop %s for upgrade: %v", name, err))
	}

	if err := tx.Activate(cmd.Context()); err != nil {
		return rollbackUpgrade(cmd, nc, tx, name, previousVersion, start, fmt.Sprintf("activate artifact: %v", err))
	}

	// Start with new version. On failure, restore the previous version
	// in state and try to bring it back up. Surface BOTH errors to the
	// user — a silent rollback-failed leaves the operator thinking the
	// node is back when in fact nothing is running.
	if err := tx.Start(cmd.Context()); err != nil {
		return rollbackUpgrade(cmd, nc, tx, name, previousVersion, start, fmt.Sprintf("start new artifact: %v", err))
	}
	if nc.Node.Runtime == "jar" {
		if err := refreshArtifactSHA256(cmd.Context(), nc); err != nil {
			// The digest may be stale, but the running version is known. Persist
			// that truth so a parent rollback can use the normal metadata path.
			nc.Node.Version = upgradeVersion
			nc.Node.PreviousVersion = previousVersion
			nc.Node.Status = "running"
			if persistErr := saveUpgradeState(nc); persistErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not persist running-version truth: %v\n", persistErr)
			}
			writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "UPGRADE_ERROR", Start: start})
			return markArtifactSwapped(exitWithError("UPGRADE_ERROR", output.ExitGeneralError, fmt.Sprintf("hash upgraded artifact: %v", err)))
		}
	}
	if err := tx.Cleanup(cmd.Context()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cleanup upgrade artifact: %v\n", err)
	}

	// Commit state only after the replacement artifact is running.
	nc.Node.PreviousVersion = previousVersion
	nc.Node.Version = upgradeVersion
	nc.Node.Status = "running"
	if err := persistNodeState("upgrade", name, nc, start, saveUpgradeState); err != nil {
		// The first save failed after activation. Retry best-effort with the
		// true running version; preserve the original loud state error.
		if persistErr := saveUpgradeState(nc); persistErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not persist running-version truth: %v\n", persistErr)
		}
		return markArtifactSwapped(err)
	}
	writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{
		"name":             name,
		"status":           "running",
		"version":          upgradeVersion,
		"previous_version": previousVersion,
	})
	return nil
}

func markArtifactSwapped(err error) error {
	if se, ok := err.(*output.StructuredError); ok {
		se.ArtifactSwapped = true
	}
	return err
}

func rollbackUpgrade(cmd *cobra.Command, nc *nodeContext, tx runtime.ArtifactTransaction, name, previousVersion string, start time.Time, cause string) error {
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
	writeAudit(auditEvent{Command: "upgrade", Node: name, Target: nc.Target.String(), Result: "rollback", ErrorCode: "UPGRADE_ERROR", Start: start})
	msg := fmt.Sprintf("Upgrade failed, rolled back to %s: %s", previousVersion, cause)
	if rollbackErr != nil {
		msg = fmt.Sprintf("%s; rollback ALSO failed: %v", msg, rollbackErr)
	}
	err := exitWithError("UPGRADE_ERROR", output.ExitGeneralError, msg, "Check logs: trond logs "+name, "Run diagnostics: trond diagnose "+name)
	if rollbackErr != nil {
		return markArtifactSwapped(err)
	}
	return err
}

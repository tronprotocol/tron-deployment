package network

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// upgradeCmd does a rolling upgrade across every node in a private
// network. The semantics differ from `trond upgrade <node>` in three
// important ways:
//
//  1. ORDER: fullnode siblings are upgraded first (one at a time),
//     then witnesses (one at a time). This minimises the chance of
//     losing block production during the rollout — witnesses are
//     the consensus producers; fullnodes can drift briefly without
//     breaking the chain.
//
//  2. GATING: each node is verified with `trond verify` before the
//     runner moves to the next. A failed verify halts the rollout
//     and surfaces which node failed (operator decides whether to
//     rollback the half-upgraded set).
//
//  3. ROLLBACK ON FAILURE: when --auto-rollback is set, the runner
//     issues `trond rollback <node>` for every successfully-upgraded
//     node before halting. Without the flag, the partial rollout
//     stays as-is and the operator is told which nodes to roll back
//     manually.
//
// We deliberately re-exec the trond binary itself for `upgrade /
// verify / rollback` rather than calling internal/apply directly.
// Each node's lifecycle is its own subprocess: clean lock semantics,
// independent logging, and the operator can ^C between nodes if the
// rollout looks wrong.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade <network-name>",
	Short: "Rolling upgrade every node in a private network",
	Long: `Upgrade every fullnode then every witness in a private network to a
new java-tron version, one node at a time, gated by per-node
verification.

Failure on any node halts the rollout. With --auto-rollback set,
all already-upgraded nodes get reverted before the command exits.
Without it, the operator is told which nodes need manual rollback.

The network name matches the intent's .name field used with
` + "`trond network create`" + `; nodes are discovered by the "<name>-nodeN"
pattern that create produces.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

var (
	upgradeVersion       string
	upgradeAutoRollback  bool
	upgradeWitnessFirst  bool
	upgradeVerifyTimeout time.Duration
	upgradeIntentPath    string
	upgradeJarURL        string
	upgradeJarSHA256     string
)

func init() {
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "",
		"Target java-tron version (required)")
	upgradeCmd.Flags().BoolVar(&upgradeAutoRollback, "auto-rollback", false,
		"On any per-node failure, revert all already-upgraded nodes before exiting")
	upgradeCmd.Flags().BoolVar(&upgradeWitnessFirst, "witness-first", false,
		"Upgrade witnesses before fullnodes (default: fullnodes first to protect block production)")
	upgradeCmd.Flags().DurationVar(&upgradeVerifyTimeout, "verify-timeout", 5*time.Minute,
		"Per-node verify timeout passed through to `trond verify`")
	upgradeCmd.Flags().StringVar(&upgradeIntentPath, "intent", "",
		"Path to the original intent.yaml used by `network create` (required for verify)")
	upgradeCmd.Flags().StringVar(&upgradeJarURL, "jar-url", "", "Target-version JAR URL (jar runtime)")
	upgradeCmd.Flags().StringVar(&upgradeJarSHA256, "jar-sha256", "", "Optional target-version JAR SHA256 (jar runtime)")
	if err := upgradeCmd.MarkFlagRequired("version"); err != nil {
		panic(err)
	}
	if err := upgradeCmd.MarkFlagRequired("intent"); err != nil {
		panic(err)
	}
	Cmd.AddCommand(upgradeCmd)
}

type upgradeStep struct {
	Node       string `json:"node"`
	Phase      string `json:"phase"`  // upgrade | verify | rollback
	Status     string `json:"status"` // ok | failed | skipped
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	networkName := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")
	start := time.Now()

	store, err := state.NewStore(paths.State())
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	st, err := store.Load()
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	witnesses, fullnodes := classifyNetworkNodes(st, networkName)
	if len(witnesses)+len(fullnodes) == 0 {
		return output.NewError("NETWORK_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("no nodes found for network %q", networkName)).
			WithSuggestions("Run `trond network status` to list deployed networks",
				"Confirm the network name matches the intent's .name field used with `network create`")
	}

	// --require-private: every node in the network must be private before we
	// stop/upgrade/restart any of them. Gather refs for the classified set.
	inNetwork := make(map[string]bool, len(witnesses)+len(fullnodes))
	for _, name := range append(append([]string{}, witnesses...), fullnodes...) {
		inNetwork[name] = true
	}
	var refs []guard.NodeRef
	for _, n := range st.Nodes {
		if inNetwork[n.Name] {
			refs = append(refs, guard.NodeRef{Name: n.Name, Network: n.Network})
		}
	}
	if err := guard.EnforceNodes(refs); err != nil {
		return err
	}

	order := append([]string{}, fullnodes...)
	order = append(order, witnesses...)
	if upgradeWitnessFirst {
		order = append([]string{}, witnesses...)
		order = append(order, fullnodes...)
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	var steps []upgradeStep
	upgraded := make([]string, 0, len(order))

	for _, node := range order {
		stepStart := time.Now()
		upgradeArgs := []string{"upgrade", node, "--version", upgradeVersion}
		if upgradeJarURL != "" {
			upgradeArgs = append(upgradeArgs, "--jar-url", upgradeJarURL)
		}
		if upgradeJarSHA256 != "" {
			upgradeArgs = append(upgradeArgs, "--jar-sha256", upgradeJarSHA256)
		}
		err, artifactSwapped := runNetworkChildResultFn(cmd.Context(), exe, st, node, upgradeArgs...)
		steps = append(steps, upgradeStep{
			Node:       node,
			Phase:      "upgrade",
			Status:     statusFor(err),
			DurationMs: time.Since(stepStart).Milliseconds(),
			Error:      errString(err),
		})
		if err != nil {
			// Rollback membership tracks mutation, not child exit status: a child
			// can swap its artifact and then fail while refreshing or persisting state.
			if artifactSwapped {
				upgraded = append(upgraded, node)
			}
			return finishWithFailure(cmd.Context(), outputFmt, networkName, exe, st,
				steps, upgraded, start, node, err)
		}
		// The artifact has been changed successfully. Keep this node in the
		// rollback set before verification: a failed health check still means
		// this node was mutated and must be restored by --auto-rollback.
		upgraded = append(upgraded, node)

		stepStart = time.Now()
		err = verifyNode(cmd.Context(), exe, node, upgradeIntentPath, upgradeVerifyTimeout)
		steps = append(steps, upgradeStep{
			Node:       node,
			Phase:      "verify",
			Status:     statusFor(err),
			DurationMs: time.Since(stepStart).Milliseconds(),
			Error:      errString(err),
		})
		if err != nil {
			return finishWithFailure(cmd.Context(), outputFmt, networkName, exe, st,
				steps, upgraded, start, node, err)
		}

	}

	result := map[string]any{
		"network":        networkName,
		"version":        upgradeVersion,
		"upgraded_count": len(upgraded),
		"upgraded_nodes": upgraded,
		"steps":          steps,
		"duration_ms":    time.Since(start).Milliseconds(),
		"status":         "success",
	}
	if warnings := cleanupNetworkBackups(cmd.Context(), st, upgraded); len(warnings) > 0 {
		result["warnings"] = warnings
	}
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, result)
	}
	fmt.Printf("Rolling upgrade of %s to %s: %d node(s) upgraded successfully (%dms)\n",
		networkName, upgradeVersion, len(upgraded), result["duration_ms"])
	return nil
}

// classifyNetworkNodes splits the discovered network into witness +
// fullnode buckets based on the persisted runtime, ordered so that
// roll-out is deterministic across runs (sorted by node name). The
// "is witness" heuristic is "name contains '-witness' or the node's
// install_path mentions 'witness'" — created by `trond network
// create`. If a future network shape is added, extend the matcher.
func classifyNetworkNodes(st *state.DeploymentState, networkName string) (witnesses, fullnodes []string) {
	prefix := networkName + "-node"
	for _, n := range st.Nodes {
		if !strings.HasPrefix(n.Name, prefix) {
			continue
		}
		if isWitnessNode(n) {
			witnesses = append(witnesses, n.Name)
		} else {
			fullnodes = append(fullnodes, n.Name)
		}
	}
	return witnesses, fullnodes
}

func isWitnessNode(n state.ManagedNode) bool {
	// Heuristics: state doesn't persist the intent's NodeSpec.Type
	// directly, so we fall back to the labels map (the create command
	// sets it when type=witness) and a name-substring check.
	if n.Labels != nil {
		if t, ok := n.Labels["tron.role"]; ok && t == "witness" {
			return true
		}
	}
	return strings.Contains(strings.ToLower(n.Name), "witness")
}

var runChild = runChildCommand
var runChildResult = runChildCommandWithResult
var runChildWithEnvResult = runChildCommandWithEnvResult
var runNetworkChildResultFn = runNetworkChildResult

const (
	networkUpgradeRestoreEnv  = "TROND_NETWORK_UPGRADE=1"
	networkUpgradePreserveEnv = "TROND_PRESERVE_BACKUP=1"
	networkUpgradeTargetEnv   = "TROND_NETWORK_UPGRADE_TARGET_VERSION"
	networkUpgradePreviousEnv = "TROND_NETWORK_UPGRADE_PREVIOUS_VERSION"
)

// rollbackChildEnv carries the node metadata from immediately before the
// attempted upgrade into the rollback child. The parent state is intentionally
// not mutated during the network upgrade, so these are the pre-attempt values.
func rollbackChildEnv(n *state.ManagedNode) []string {
	return []string{
		networkUpgradeRestoreEnv,
		networkUpgradeTargetEnv + "=" + n.Version,
		networkUpgradePreviousEnv + "=" + n.PreviousVersion,
	}
}

func runChildCommand(ctx context.Context, exe string, argv ...string) error {
	err, _ := runChildCommandWithEnvResult(ctx, exe, nil, argv...)
	return err
}

func runChildCommandWithResult(ctx context.Context, exe string, argv ...string) (error, bool) {
	return runChildCommandWithEnvResult(ctx, exe, nil, argv...)
}

func runChildCommandWithEnvResult(ctx context.Context, exe string, extraEnv []string, argv ...string) (error, bool) {
	argv = append(argv, "--output", "json")
	cmd := exec.CommandContext(ctx, exe, argv...)
	if len(extraEnv) > 0 {
		cmd.Env = childEnv(extraEnv)
	}
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	artifactSwapped := false
	// Errors are emitted on stderr by the root command, while successful
	// results are emitted on stdout. Parse both so upgrade failures can report
	// whether the artifact was already activated. Malformed/absent envelopes
	// conservatively exclude the node from rollback; this is residual risk.
	envelope := stdout.Bytes()
	if runErr != nil {
		envelope = stderr.Bytes()
	}
	lines := bytes.Split(envelope, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var value map[string]any
		if json.Unmarshal(bytes.TrimSpace(lines[i]), &value) == nil {
			if _, ok := value["error_code"]; ok {
				artifactSwapped, _ = value["artifact_swapped"].(bool)
				break
			}
		}
	}
	if runErr != nil {
		if stderr.Len() > 0 {
			suffix := ""
			if stderr.limited || stderr.total > 1<<20 || stderr.Len() >= 1<<20 {
				suffix = " [truncated at 1MiB]"
			}
			text := stderr.String()
			if (stderr.limited || stderr.total > 1<<20 || stderr.Len() >= 1<<20) && len(text) > 256 {
				text = text[:256]
			}
			return fmt.Errorf("child command failed: %w: %s%s", runErr, text, suffix), artifactSwapped
		}
		return runErr, artifactSwapped
	}
	var value any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		return fmt.Errorf("child command returned invalid JSON: %w", err), false
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("child command returned JSON that is not an object"), false
	}
	if stdout.limited || stderr.limited {
		return fmt.Errorf("child command output exceeds 1MiB limit"), false
	}
	return nil, artifactSwapped
}

func childEnv(extraEnv []string) []string {
	baseEnv := make([]string, 0, len(os.Environ())+len(extraEnv))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "TROND_NETWORK_UPGRADE", networkUpgradeTargetEnv, networkUpgradePreviousEnv, "TROND_PRESERVE_BACKUP":
			continue
		}
		baseEnv = append(baseEnv, entry)
	}
	return append(baseEnv, extraEnv...)
}

type limitedBuffer struct {
	bytes.Buffer
	limited bool
	total   int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const max = 1 << 20
	b.total += len(p)
	if b.total > max {
		b.limited = true
	}
	if b.Len() >= max {
		b.limited = true
		return len(p), nil
	}
	if len(p) > max-b.Len() {
		_, _ = b.Buffer.Write(p[:max-b.Len()])
		b.limited = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// verifyNode projects the multi-node intent to the node being upgraded before
// invoking the existing single-node verify command. Verify reads Nodes[0], so
// passing the original multi-node intent would silently verify only node 0.
func verifyNode(ctx context.Context, exe, node, intentPath string, timeout time.Duration) error {
	parsed, err := intent.Load(intentPath)
	if err != nil {
		return fmt.Errorf("load verify intent: %w", err)
	}
	var projected *intent.Intent
	for i := range parsed.Nodes {
		if fmt.Sprintf("%s-node%d", parsed.Name, i) == node {
			projected, _, _, err = nodeIntent(parsed, i)
			if err != nil {
				return fmt.Errorf("project node %s for verify: %w", node, err)
			}
			break
		}
	}
	if projected == nil {
		return fmt.Errorf("node %q is not present in verify intent", node)
	}
	data, err := yaml.Marshal(projected)
	if err != nil {
		return fmt.Errorf("marshal verify intent for %s: %w", node, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(intentPath), ".trond-verify-*.yaml")
	if err != nil {
		return fmt.Errorf("create verify intent for %s: %w", node, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write verify intent for %s: %w", node, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close verify intent for %s: %w", node, err)
	}
	return runChild(ctx, exe, "verify", "--intent", tmpPath, "--timeout", timeout.String())
}

func statusFor(err error) string {
	if err == nil {
		return "ok"
	}
	return "failed"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// finishWithFailure surfaces the partial-rollout state. When
// --auto-rollback is set, every already-upgraded node gets a rollback
// step appended; otherwise the operator is told which nodes need
// manual rollback.
func finishWithFailure(ctx context.Context, outputFmt, networkName, exe string,
	st *state.DeploymentState,
	steps []upgradeStep, upgraded []string, start time.Time, failedNode string, failErr error) error {
	rolledBack := make([]string, 0, len(upgraded))
	if upgradeAutoRollback {
		for _, node := range upgraded {
			stepStart := time.Now()
			err := runNetworkChild(ctx, exe, st, node, "rollback", node)
			steps = append(steps, upgradeStep{
				Node:       node,
				Phase:      "rollback",
				Status:     statusFor(err),
				DurationMs: time.Since(stepStart).Milliseconds(),
				Error:      errString(err),
			})
			if err == nil {
				rolledBack = append(rolledBack, node)
			}
		}
	}

	result := map[string]any{
		"network":           networkName,
		"version":           upgradeVersion,
		"upgraded_nodes":    upgraded,
		"rolled_back_nodes": rolledBack,
		"failed_at":         failedNode,
		"steps":             steps,
		"duration_ms":       time.Since(start).Milliseconds(),
		"status":            "failed",
	}
	if outputFmt == "json" {
		_ = output.WriteJSON(os.Stdout, result)
	}

	se := output.NewError("UPGRADE_FAILED", output.ExitGeneralError,
		fmt.Sprintf("network upgrade halted at node %s: %v", failedNode, failErr))
	if upgradeAutoRollback {
		se = se.WithSuggestions(
			fmt.Sprintf("Auto-rollback ran for %d node(s); inspect logs", len(rolledBack)),
			"Investigate why "+failedNode+" failed before retrying",
		)
	} else {
		hint := "no auto-rollback ran"
		if len(upgraded) > 0 {
			hint += "; upgraded nodes still on new version: " + strings.Join(upgraded, ", ")
		}
		se = se.WithSuggestions(
			hint,
			"Re-run with --auto-rollback to revert on next failure, or `trond rollback <node>` per node",
		)
	}
	return se
}

func runNetworkChild(ctx context.Context, exe string, st *state.DeploymentState, node string, argv ...string) error {
	err, _ := runNetworkChildResult(ctx, exe, st, node, argv...)
	return err
}

func runNetworkChildResult(ctx context.Context, exe string, st *state.DeploymentState, node string, argv ...string) (error, bool) {
	if st != nil {
		for _, n := range st.Nodes {
			if n.Name != node || n.Runtime != "jar" || len(argv) == 0 {
				continue
			}
			switch argv[0] {
			case "rollback":
				return runChildWithEnvResult(ctx, exe, rollbackChildEnv(&n), argv...)
			case "upgrade":
				return runChildWithEnvResult(ctx, exe, []string{networkUpgradePreserveEnv}, argv...)
			}
		}
	}
	return runChildResult(ctx, exe, argv...)
}

var fromManagedNode = target.FromManagedNode

func cleanupNetworkBackup(ctx context.Context, st *state.DeploymentState, node string) error {
	var managedNode *state.ManagedNode
	for i := range st.Nodes {
		if st.Nodes[i].Name == node && st.Nodes[i].Runtime == "jar" {
			managedNode = &st.Nodes[i]
			break
		}
	}
	if managedNode == nil {
		return nil
	}

	path := filepath.Join(managedNode.InstallPath, "FullNode.jar.upgrade.backup")
	tgt, err := fromManagedNode(managedNode)
	if err != nil {
		return err
	}
	if closer, ok := tgt.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	_, err = tgt.Exec(ctx, "rm", "-f", "--", path)
	return err
}

func cleanupNetworkBackups(ctx context.Context, st *state.DeploymentState, nodes []string) []string {
	var warnings []string
	for _, node := range nodes {
		if err := cleanupNetworkBackup(ctx, st, node); err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to remove upgrade backup for %s: %v", node, err))
		}
	}
	return warnings
}

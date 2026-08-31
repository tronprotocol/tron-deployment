package network

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

type destroyFailure struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

var destroyConfirm string

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Destroy a private network",
	Long:  "Stop and remove all nodes belonging to a network.",
	RunE:  runDestroy,
}

func init() {
	destroyCmd.Flags().StringVar(&destroyConfirm, "confirm", "", "Confirm by repeating the network name")
}

func runDestroy(cmd *cobra.Command, args []string) error {
	_, _ = cmd.Flags().GetString("output")
	start := time.Now()

	if destroyConfirm == "" {
		return output.NewError("HUMAN_REQUIRED", output.ExitHumanRequired,
			"Destructive operation: destroying network").
			WithSuggestions("Add --confirm <network-name> to proceed")
	}

	// Hold the state lock across the whole load-modify-save cycle: this
	// command reads the node list here and writes it back much later, and
	// a concurrent trond would otherwise drop one of the two updates.
	lock := state.NewLock(paths.BaseDir())
	if err := lock.Acquire(); err != nil {
		return output.NewError("LOCK_ERROR", output.ExitGeneralError, "acquire state lock: "+err.Error())
	}
	defer lock.Release()

	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}

	deployState, err := store.Load()
	if err != nil {
		return err
	}

	// Pre-flight: refuse to "destroy" a name that owns zero nodes — almost
	// always a typo (e.g. `--confirm=wrng` instead of the real network).
	// Returning success on a no-op silently masks the mistake.
	prefix := destroyConfirm + "-node"
	matchesAny := false
	for _, n := range deployState.Nodes {
		if strings.HasPrefix(n.Name, prefix) || n.Name == destroyConfirm {
			matchesAny = true
			break
		}
	}
	if !matchesAny {
		return output.NewError("NETWORK_NOT_FOUND", output.ExitGeneralError,
			"no network named "+destroyConfirm+" — nothing to destroy").
			WithSuggestions("Run: trond network status",
				"Run: trond list  (to see all managed nodes)")
	}

	// --require-private: every node in the network must be private before we
	// tear anything down. Gathered from the same prefix match used above so
	// the refusal happens before any node is removed.
	var refs []guard.NodeRef
	for _, n := range deployState.Nodes {
		if strings.HasPrefix(n.Name, prefix) || n.Name == destroyConfirm {
			refs = append(refs, guard.NodeRef{Name: n.Name, Network: n.Network})
		}
	}
	if err := guard.EnforceNodes(refs); err != nil {
		return err
	}

	// Keep the recorded target for network-level cleanup before removing the
	// nodes from state. Never fall back to the operator's local Docker daemon.
	// Network create/add allow mixed targets. Keep one target per distinct
	// target identity and run network-level cleanup on every one; this avoids
	// assuming the first node represents the whole network.
	networkTargets, targetFailures, failedTargetKeys := collectNetworkTargets(deployState.Nodes, prefix, destroyConfirm)

	var removed []string
	var failures []destroyFailure
	failures = append(failures, targetFailures...)
	auditResult := "success"
	auditTarget := ""

	// Find and remove all network nodes. Each node is removed using the
	// target type recorded in state — not a hard-coded LocalTarget — so a
	// network deployed over SSH actually tears down its remote
	// containers. Errors per node are captured and surfaced; we keep
	// going so a single failure doesn't strand siblings.
	for i := len(deployState.Nodes) - 1; i >= 0; i-- {
		n := deployState.Nodes[i]
		if !(strings.HasPrefix(n.Name, prefix) || n.Name == destroyConfirm) {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d|%s|%s", n.Target.Type, n.Target.Host, n.Target.Port, n.Target.User, n.Target.IdentityFile)
		if failedTargetKeys[key] {
			continue
		}
		auditTarget = n.Target.Type // any matching node's type — they're all the same in practice

		tgt, terr := resolveTargetForNode(&n)
		if terr != nil {
			failures = append(failures, destroyFailure{Name: n.Name, Error: terr.Error()})
			continue
		}
		rt := resolveRuntimeForNode(&n, tgt)
		if rerr := rt.Remove(cmd.Context(), n.Name, true); rerr != nil {
			failures = append(failures, destroyFailure{Name: n.Name, Error: rerr.Error()})
			closeTarget(tgt)
			continue
		}
		closeTarget(tgt)
		removed = append(removed, n.Name)
		store.RemoveNode(deployState, n.Name)
	}

	if err := store.Save(deployState); err != nil {
		closeTargets(networkTargets)
		// State save failure is rare but real; surface it instead of
		// claiming success with a stale state file on disk.
		return output.NewError("STATE_ERROR", output.ExitGeneralError,
			"failed to persist state after destroy: "+err.Error())
	}

	// Remove monitoring stack if it exists (best-effort) on the recorded target.
	for _, networkTarget := range networkTargets {
		monRT := runtime.NewMonitoringRuntime(networkTarget, paths.Deployments())
		if err := monRT.Remove(cmd.Context(), destroyConfirm, true); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove monitoring stack: %v\n", err)
		}
	}
	// Tear down the shared docker network the matching `network create`
	// stood up. Best-effort: a leftover network is harmless on next
	// run (create re-uses it), so we ignore failures rather than
	// surfacing them as the destroy result.
	if len(removed) > 0 {
		for _, networkTarget := range networkTargets {
			_, _ = networkTarget.Exec(cmd.Context(), "docker", "network", "rm", "trond-"+destroyConfirm)
		}
	}
	closeTargets(networkTargets)

	if len(failures) > 0 {
		auditResult = "partial"
	}
	writeAudit(auditEvent{
		Command: "network-destroy",
		Node:    destroyConfirm,
		Target:  auditTarget,
		Result:  auditResult,
		Start:   start,
	})

	result := map[string]any{
		"network": destroyConfirm,
		"removed": removed,
	}
	if len(failures) > 0 {
		result["failed"] = failures
	}
	output.WriteJSON(os.Stdout, result)
	if len(failures) > 0 {
		return output.NewError("PARTIAL_SUCCESS", output.ExitGeneralError,
			fmt.Sprintf("partial success: %d node(s) failed to destroy; removed entries are gone from state, failed entries REMAIN in state and can be retried", len(failures)))
	}
	return nil
}

var resolveTargetForDestroy = resolveTargetForNode

func collectNetworkTargets(nodes []state.ManagedNode, prefix, confirm string) (map[string]target.Target, []destroyFailure, map[string]bool) {
	networkTargets := make(map[string]target.Target)
	var targetFailures []destroyFailure
	failedTargetKeys := make(map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		if !(strings.HasPrefix(n.Name, prefix) || n.Name == confirm) {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d|%s|%s", n.Target.Type, n.Target.Host, n.Target.Port, n.Target.User, n.Target.IdentityFile)
		if failedTargetKeys[key] {
			continue
		}
		if _, ok := networkTargets[key]; ok {
			continue
		}
		tgt, err := resolveTargetForDestroy(n)
		if err != nil {
			targetFailures = append(targetFailures, targetResolutionFailures(nodes, prefix, confirm, key, err)...)
			failedTargetKeys[key] = true
			continue
		}
		networkTargets[key] = tgt
	}
	return networkTargets, targetFailures, failedTargetKeys
}

func targetResolutionFailures(nodes []state.ManagedNode, prefix, confirm, key string, err error) []destroyFailure {
	var failures []destroyFailure
	for _, node := range nodes {
		nodeKey := fmt.Sprintf("%s|%s|%d|%s|%s", node.Target.Type, node.Target.Host, node.Target.Port, node.Target.User, node.Target.IdentityFile)
		if (strings.HasPrefix(node.Name, prefix) || node.Name == confirm) && nodeKey == key {
			failures = append(failures, destroyFailure{Name: node.Name, Error: "target cleanup: " + err.Error()})
		}
	}
	return failures
}

// resolveTargetForNode returns the right Target for a managed node.
// Mirrors cmd/resolve.go but lives here to avoid the cmd ↔ cmd/network
// import cycle. SSH targets are connected eagerly so callers don't have
// to remember to dial.
func resolveTargetForNode(n *state.ManagedNode) (target.Target, error) {
	switch n.Target.Type {
	case "ssh":
		s := target.NewSSHTarget(n.Target.Host, n.Target.Port, n.Target.User, n.Target.IdentityFile)
		if err := s.Connect(); err != nil {
			return nil, fmt.Errorf("ssh connect to %s: %w", n.Target.Host, err)
		}
		return s, nil
	default:
		return target.NewLocalTarget(), nil
	}
}

func resolveRuntimeForNode(n *state.ManagedNode, tgt target.Target) runtime.Runtime {
	if n.Runtime == "jar" {
		jr := runtime.NewJarRuntime(tgt)
		if n.InstallPath != "" {
			jr.SetPurgeInstallPath(n.InstallPath)
		}
		return jr
	}
	return runtime.NewDockerRuntime(tgt, paths.Deployments())
}

func closeTarget(t target.Target) {
	if c, ok := any(t).(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func closeTargets(targets map[string]target.Target) {
	for _, tgt := range targets {
		closeTarget(tgt)
	}
}

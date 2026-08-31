package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var statusCmd = &cobra.Command{
	Use:   "status [node]",
	Short: "Show node status (or list all nodes)",
	Long: `Without arguments: list all managed nodes. With a node name: show
detailed status including (best-effort) live block height, peer count,
sync state, and the running endpoints. Network probes use the same
HTTP API endpoints inspect/diagnose use; failures are surfaced inline
rather than failing the whole command.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	if len(args) == 0 {
		return runList(cmd, args)
	}

	name := args[0]

	store, err := state.NewStore(statePath())
	if err != nil {
		return err
	}

	deployState, err := store.Load()
	if err != nil {
		return err
	}

	node := store.GetNode(deployState, name)
	if node == nil {
		return exitWithError("NODE_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("Node %q not found", name),
			"Run: trond list")
	}

	// Build the contract-shaped response. The CLI contract
	// (specs/.../contracts/cli-contract.md) and knowledge/test-harness.md
	// promise block_height, peer_count, sync_progress_percent, is_synced,
	// uptime, api_endpoints — populate them when reachable, leave the
	// keys absent when the API isn't (so JSON consumers can distinguish
	// "not yet healthy" from "I forgot the field").
	statusInfo := map[string]any{
		"name":         node.Name,
		"status":       node.Status,
		"runtime":      node.Runtime,
		"version":      node.Version,
		"target":       node.Target,
		"last_applied": node.LastApplied,
		"intent_hash":  node.IntentHash,
		"config_hash":  node.ConfigHash,
		// Network + is_private let an automated caller PROVE a rig is a
		// private net before acting (the C1 safety fact). network is
		// empty for nodes deployed before it was recorded; is_private is
		// then false (fail-safe — an agent treats "unknown" as not-safe).
		"network":    node.Network,
		"is_private": intent.IsPrivate(node.Network),
		// healthy is the single boolean an agent gates on for "is this
		// node answering RPC". Seeded false so the field is always present
		// and fail-safe; the live probe flips it true only on a parseable
		// getnowblock. logs tells an external consumer (mcp-logs) how to
		// read this node's logs without screen-scraping. Both are A1.
		"healthy": false,
		"logs":    apply.LogsDescriptor(node),
		"api_endpoints": map[string]any{
			"http": apply.HTTPURL(target.EndpointHost(node.Target.Type, node.Target.Host), apply.PortOrDefault(node.HTTPPort, 8090)),
			"grpc": apply.GRPCAddr(target.EndpointHost(node.Target.Type, node.Target.Host), apply.PortOrDefault(node.GRPCPort, 50051)),
		},
	}

	// Build identity (B1): when the node was built from source, surface the
	// content-addressed cache key AND the resolved git revision, so an agent
	// knows exactly which java-tron commit is running without assuming.
	// Omitted entirely for nodes that consumed a pre-built image/jar.
	if node.BuildCacheKey != "" {
		statusInfo["build_cache_key"] = node.BuildCacheKey
		if rev := node.BuildRevision(); rev != "" {
			statusInfo["build_revision"] = rev
		}
	}

	// Resolve the target at most once, and only when we actually need it.
	// Resolving is cheap for a local target but opens an SSH connection for
	// an ssh one — and SSHTarget.Connect() is NOT bound by the 3s context
	// below — so we must never resolve just to fetch container_id from a
	// stopped remote node (that would turn an instant `status` into a
	// blocking SSH dial). We resolve when:
	//   - the node is running (the live probe needs a target regardless), or
	//   - it's a LOCAL docker node (cheap; lets us read container_id even
	//     when stopped — a stopped container still has an ID).
	// An ssh docker node still gets container_id, but only piggybacked on
	// the running-node probe resolution — never via a fresh dial for a
	// stopped node. Best-effort throughout: failure just omits the fields.
	needLocalDockerID := node.Runtime == "docker" && node.Target.Type == "local"
	if node.Status == "running" || needLocalDockerID {
		if tgt, err := resolveTargetFromNode(node); err == nil {
			if c, ok := any(tgt).(interface{ Close() error }); ok {
				defer c.Close()
			}
			// Live probe FIRST and with its own 3s budget: `healthy` is the
			// primary signal, and it must not be starved by a slow
			// `docker inspect` for the optional container_id. Each call gets
			// an independent deadline off the command context.
			if node.Status == "running" {
				pctx, pcancel := context.WithTimeout(cmd.Context(), 3*time.Second)
				maps.Copy(statusInfo, apply.LiveStatus(pctx, tgt, node))
				pcancel()
			}
			// container_id (optional metadata) lets an agent attach/exec
			// against the exact container. ContainerID no-ops for non-docker
			// nodes, so it's safe to call unconditionally on the resolved
			// target.
			cctx, ccancel := context.WithTimeout(cmd.Context(), 3*time.Second)
			if id := apply.ContainerID(cctx, tgt, node); id != "" {
				statusInfo["container_id"] = id
			}
			ccancel()
		}
	}

	if node.Monitoring != nil && node.Monitoring.Enabled {
		statusInfo["monitoring"] = map[string]any{
			"prometheus_port": node.Monitoring.PrometheusPort,
			"grafana_port":    node.Monitoring.GrafanaPort,
			"status":          probeMonitoringStatus(cmd.Context(), node.Name),
		}
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, statusInfo)
	}

	fmt.Printf("Node:         %s\n", node.Name)
	fmt.Printf("Status:       %s\n", node.Status)
	fmt.Printf("Runtime:      %s\n", node.Runtime)
	fmt.Printf("Version:      %s\n", node.Version)
	fmt.Printf("Target:       %s\n", node.Target.Type)
	fmt.Printf("Last Applied: %s\n", node.LastApplied.Format("2006-01-02 15:04:05 UTC"))
	if h, ok := statusInfo["block_height"].(int64); ok {
		fmt.Printf("Block height: %d\n", h)
	}
	if p, ok := statusInfo["peer_count"].(int); ok {
		fmt.Printf("Peers:        %d\n", p)
	}
	if syn, ok := statusInfo["is_synced"].(bool); ok {
		fmt.Printf("Synced:       %v\n", syn)
	}
	if h, ok := statusInfo["healthy"].(bool); ok {
		fmt.Printf("Healthy:      %v\n", h)
	}
	if id, ok := statusInfo["container_id"].(string); ok && id != "" {
		fmt.Printf("Container:    %s\n", id[:12])
	}

	// Show monitoring stack health if deployed.
	if node.Monitoring != nil && node.Monitoring.Enabled {
		monStatus := probeMonitoringStatus(cmd.Context(), node.Name)
		fmt.Printf("Monitoring:   %s (prometheus :%d, grafana :%d)\n",
			monStatus, node.Monitoring.PrometheusPort, node.Monitoring.GrafanaPort)
	}

	return nil
}

// probeMonitoringStatus returns a short health status string for the
// monitoring stack deployed alongside the named node. Best-effort —
// returns "unknown" when the stack can't be reached.
func probeMonitoringStatus(ctx context.Context, name string) string {
	monRT := runtime.NewMonitoringRuntime(target.NewLocalTarget(), paths.Deployments())
	status, err := monRT.Status(ctx, name)
	if err != nil || status == nil {
		return "unknown"
	}
	return status.Status
}

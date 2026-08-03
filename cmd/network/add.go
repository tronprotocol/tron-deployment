package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var (
	addNetworkName string
	addIntentPath  string
)

var addCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a node to an existing private network",
	Long: `Append one node to an already-running private network.

The intent file may describe just the node (single entry under nodes:) — the
top-level name/network/target are taken from the existing enclave when the
intent omits them. The new node is named "<network>-node<i>" with i chosen
as the next free index.`,
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringVar(&addNetworkName, "network", "", "Network name (the prefix used by 'network create')")
	addCmd.Flags().StringVar(&addIntentPath, "intent", "", "Path to a single-node intent.yaml")
	if err := addCmd.MarkFlagRequired("network"); err != nil {
		panic(err)
	}
	if err := addCmd.MarkFlagRequired("intent"); err != nil {
		panic(err)
	}
	Cmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	start := time.Now()

	parsed, err := intent.Load(addIntentPath)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}
	if len(parsed.Nodes) != 1 {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"network add expects an intent with exactly one node entry")
	}
	node := &parsed.Nodes[0]

	// --require-private: refuse to add a node on a non-private network.
	if err := guard.Enforce(parsed.Network); err != nil {
		return err
	}

	// Pick the next free index. Existing entries are "<network>-node<N>"; we
	// rescan state instead of trusting any in-memory counter so the operation
	// is safe to retry.
	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}
	deployState, err := store.Load()
	if err != nil {
		return err
	}

	prefix := addNetworkName + "-node"
	nextIdx := 0
	for _, n := range deployState.Nodes {
		if !strings.HasPrefix(n.Name, prefix) {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(n.Name, prefix+"%d", &idx); err == nil && idx >= nextIdx {
			nextIdx = idx + 1
		}
	}
	nodeName := fmt.Sprintf("%s%d", prefix, nextIdx)

	// Resolve target by type. SECURITY: previously this always built a
	// LocalTarget regardless of intent.target.type, which silently sent
	// SSH-intended deploys to the local host (and combined with any YAML
	// injection in the rendered compose, that ran on the operator's
	// machine instead of the remote target).
	var tgt target.Target
	switch parsed.Target.Type {
	case "ssh":
		s := target.NewSSHTarget(parsed.Target.Host, parsed.Target.Port,
			parsed.Target.User, parsed.Target.IdentityFile)
		if err := s.Connect(); err != nil {
			return output.NewError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
		}
		tgt = s
	default:
		tgt = target.NewLocalTarget()
	}
	defer func() {
		if c, ok := any(tgt).(interface{ Close() error }); ok {
			c.Close()
		}
	}()

	// Auto-attach the shared docker network so the new node can resolve
	// sibling container names for P2P peering. Without this it lands only
	// on its per-compose bridge and stays at 0 peers.
	sharedNet := "trond-" + addNetworkName
	if !slices.Contains(node.Networks, sharedNet) {
		node.Networks = append(node.Networks, sharedNet)
	}

	// Auto-populate active_peers from existing nodes in the network so the
	// new node can dial into the running mesh. P2P connections are
	// bidirectional once established, so we only update the new node —
	// no need to reconfigure (and restart) existing siblings, they'll
	// accept the incoming connection. Skip nodes whose P2PPort is zero
	// (legacy state predating the field) and skip when the user
	// explicitly supplied active_peers in the intent.
	if node.NetworkOverrides.ActivePeers == nil {
		var existingPeers []string
		for _, n := range deployState.Nodes {
			if !strings.HasPrefix(n.Name, prefix) {
				continue
			}
			if n.P2PPort == 0 {
				continue
			}
			existingPeers = append(existingPeers, fmt.Sprintf("%s:%d", n.Name, n.P2PPort))
		}
		if len(existingPeers) > 0 {
			node.NetworkOverrides.ActivePeers = &existingPeers
		}
	}

	// If the network already has monitoring deployed, auto-enable metrics
	// for the new node so Prometheus can scrape it.
	monCompose := filepath.Join(paths.Deployments(), addNetworkName+"-monitoring", "docker-compose.yaml")
	if _, err := os.Stat(monCompose); err == nil {
		enabled := true
		parsed.Monitoring = &intent.Monitoring{Enabled: &enabled}
		intent.ApplyMonitoringDefaults(parsed.Monitoring)
	}

	templateDir := findTemplatesDir()
	rendered, err := render.RenderHOCONWithSecrets(templateDir, parsed, node)
	if err != nil {
		return fmt.Errorf("render config: %w", err)
	}
	// Deploy path — needs the real witness key inlined.
	hocon := rendered.Deployable()

	memGB := render.ParseMemoryGB(node.Resources.Memory)
	if memGB == 0 {
		memGB = 16
	}
	jvmArgs := render.JVMArgsString(memGB, 17, node.JVM)
	composeYAML := render.RenderCompose(nodeName, parsed, node, "", jvmArgs, "")

	rt := runtime.NewDockerRuntime(tgt, paths.Deployments())
	opts := runtime.DeployOpts{
		Name:        nodeName,
		ConfigData:  []byte(hocon),
		ComposeData: []byte(composeYAML),
	}
	if err := rt.Deploy(cmd.Context(), opts); err != nil {
		return output.NewError("DEPLOY_ERROR", output.ExitGeneralError, err.Error())
	}

	store.UpsertNode(deployState, state.ManagedNode{
		Name:    nodeName,
		Version: node.Version,
		// Record the network kind (mainnet|nile|private) so the C1
		// is_private fact and the --require-private guard work for added
		// nodes too — without this they'd read as legacy/unknown and a
		// private-net node would be fail-safe-refused by every mutator.
		Network: parsed.Network,
		// Persist the FULL target so subsequent stop/start/files/inspect
		// can rebuild the SSH connection. Earlier this only stored
		// Type, leaving Host/User/Port/IdentityFile blank — which then
		// silently fell through to LocalTarget on follow-up commands.
		Target: state.NodeTarget{
			Type:         parsed.Target.Type,
			Host:         parsed.Target.Host,
			User:         parsed.Target.User,
			Port:         parsed.Target.Port,
			IdentityFile: parsed.Target.IdentityFile,
		},
		Runtime:     "docker",
		Status:      "running",
		LastApplied: time.Now().UTC(),
		HTTPPort:    node.Ports.HTTP,
		GRPCPort:    node.Ports.GRPC,
		P2PPort:     node.Ports.P2P,
		MetricsPort: node.Ports.Metrics,
		InstallPath: node.InstallPath,
		Labels:      node.Labels,
	})
	if err := store.Save(deployState); err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError,
			"failed to persist state: "+err.Error())
	}

	// Reload monitoring stack to include the new node if already deployed.
	reloadNetworkMonitoring(cmd.Context(), addNetworkName, deployState, tgt)
	writeAudit(auditEvent{
		Command: "network-add",
		Node:    nodeName,
		Target:  parsed.Target.Type, // honors the intent target, not hardcoded "local"
		Result:  "success",
		Start:   start,
	})

	result := map[string]any{
		"network": addNetworkName,
		"added":   nodeName,
		"endpoints": map[string]string{
			"http": fmt.Sprintf("http://127.0.0.1:%d", node.Ports.HTTP),
			"grpc": fmt.Sprintf("127.0.0.1:%d", node.Ports.GRPC),
		},
	}
	output.WriteJSON(os.Stdout, result)
	return nil
}

// metricsPort returns the Prometheus scrape port stored in state, falling
// back to the default 9527 for nodes deployed before MetricsPort was tracked.
// This allows network add to build correct scrape targets even when the user
// overrides ports.metrics in the intent.
func metricsPort(n state.ManagedNode) int {
	if n.MetricsPort != 0 {
		return n.MetricsPort
	}
	return 9527
}

// reloadNetworkMonitoring updates the Prometheus scrape config to include
// a newly added node, then reloads Prometheus. Best-effort: failure is
// silent (monitoring can be re-synced on next network create --monitor).
func reloadNetworkMonitoring(ctx context.Context, networkName string, deployState *state.DeploymentState, tgt target.Target) {
	prefix := networkName + "-node"
	monDir := paths.Deployments()
	monCompose := filepath.Join(monDir, networkName+"-monitoring", "docker-compose.yaml")

	// Check if monitoring is deployed for this network.
	if _, err := os.Stat(monCompose); err != nil {
		return
	}

	// Collect all nodes in the network.
	var targets []render.MonitoringTarget
	for _, n := range deployState.Nodes {
		if !strings.HasPrefix(n.Name, prefix) {
			continue
		}
		targets = append(targets, render.MonitoringTarget{
			Name:    n.Name,
			Address: fmt.Sprintf("%s:%d", n.Name, metricsPort(n)),
			Labels: map[string]string{
				"group":    "group-tron",
				"instance": n.Name,
				"network":  "private",
			},
		})
	}
	if len(targets) == 0 {
		return
	}

	// Regenerate prometheus config.  Retention is controlled by the
	// docker-compose command args, not prometheus.yml, so we pass an
	// empty string here — the existing compose file is left untouched.
	promPath := filepath.Join(monDir, networkName+"-monitoring", "conf", "prometheus.yml")
	promConfig := render.RenderPrometheusConfig(targets, "")
	if err := tgt.WriteFile(ctx, promPath, []byte(promConfig), 0644); err != nil {
		return
	}

	// Reload Prometheus via SIGHUP.
	_, _ = tgt.Exec(ctx, "docker", "exec", networkName+"-prometheus", "kill", "-HUP", "1")
}

package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	createIntentPath string
	createMonitor    bool
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a private network from an intent file",
	Long:  "Deploy all nodes defined in the intent file, wiring peer connections via seed node config.",
	RunE:  runCreate,
}

func init() {
	createCmd.Flags().StringVar(&createIntentPath, "intent", "", "Path to intent.yaml (required)")
	createCmd.Flags().BoolVar(&createMonitor, "monitor", false, "Deploy Prometheus + Grafana monitoring stack for the network")
	// --require-private is the persistent root flag + TROND_REQUIRE_PRIVATE
	// env, enforced via internal/guard — inherited here, no local flag.
	if err := createCmd.MarkFlagRequired("intent"); err != nil {
		panic(err)
	}
}

func runCreate(cmd *cobra.Command, args []string) error {
	start := time.Now()

	parsed, err := intent.Load(createIntentPath)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// --require-private: same machine-enforced safety gate as `apply`,
	// applied to the multi-node path. Refuse a non-private network before
	// deploying anything. Predicate + error live in internal/guard.
	if err := guard.Enforce(parsed.Network); err != nil {
		return err
	}

	// Apply monitoring defaults early so RenderHOCON can inject metrics config.
	if createMonitor {
		if parsed.Monitoring == nil {
			parsed.Monitoring = &intent.Monitoring{}
		}
		parsed.Monitoring.Enabled = intent.BoolPtr(true)
		intent.ApplyMonitoringDefaults(parsed.Monitoring)
	}

	// Resolve target
	var tgt target.Target
	switch parsed.Target.Type {
	case "ssh":
		t := target.NewSSHTarget(parsed.Target.Host, parsed.Target.Port, parsed.Target.User, parsed.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return output.NewError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
		}
		defer t.Close()
		tgt = t
	default:
		tgt = target.NewLocalTarget()
	}

	templateDir := findTemplatesDir()
	workDir := paths.Deployments()

	store, err := state.NewStore(paths.State())
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	deployState, err := store.Load()
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	// Auto-wire peering between siblings before rendering. After
	// intent.Load() ports are final (defaults applied, auto_ports
	// resolved), so we can synthesise stable inter-container addresses.
	// Each node's network_overrides.active_peers is set to all OTHER
	// nodes' "<name>:<p2p_port>" — except when the user supplied an
	// explicit list, which we never override.
	autoWireActivePeers(parsed)

	// Each node deploys via its own `docker compose -p <node_name> up`,
	// which gives it a per-project bridge network. That isolates the
	// nodes from each other — they can't resolve sibling container
	// names. We solve it by creating one shared user-defined network
	// up front (`trond-<intent.name>`) and wiring every rendered
	// compose file to attach to it via an external-network reference.
	// docker network create is idempotent enough: re-creating returns
	// non-zero, which we tolerate as "already exists".
	sharedNet := "trond-" + parsed.Name
	if _, err := tgt.Exec(cmd.Context(), "docker", "network", "inspect", sharedNet); err != nil {
		if _, err := tgt.Exec(cmd.Context(), "docker", "network", "create", sharedNet); err != nil {
			return output.NewError("DEPLOY_ERROR", output.ExitGeneralError,
				"create shared docker network "+sharedNet+": "+err.Error()).
				WithSuggestions("Confirm Docker is running",
					"Try `docker network rm "+sharedNet+"` then re-run `network create`")
		}
	}
	// Auto-attach the shared network to every node's compose so peer
	// resolution works. Preserve any networks the user already declared.
	for i := range parsed.Nodes {
		n := &parsed.Nodes[i]
		if !slices.Contains(n.Networks, sharedNet) {
			n.Networks = append(n.Networks, sharedNet)
		}
	}

	var deployed []map[string]any

	for i, node := range parsed.Nodes {
		nodeName := fmt.Sprintf("%s-node%d", parsed.Name, i)

		rendered, err := render.RenderHOCONWithSecrets(templateDir, parsed, &node)
		if err != nil {
			return fmt.Errorf("render config for node %d: %w", i, err)
		}
		// Deploy path — needs the real witness key inlined.
		hocon := rendered.Deployable()

		memGB := render.ParseMemoryGB(node.Resources.Memory)
		if memGB == 0 {
			memGB = 16
		}
		jvmArgs := render.JVMArgsString(memGB, 17, node.JVM)
		composeYAML := render.RenderCompose(nodeName, parsed, &node, "", jvmArgs, "")

		opts := runtime.DeployOpts{
			Name:        nodeName,
			ConfigData:  []byte(hocon),
			ComposeData: []byte(composeYAML),
		}

		rt := runtime.NewDockerRuntime(tgt, workDir)
		if err := rt.Deploy(cmd.Context(), opts); err != nil {
			deployed = append(deployed, map[string]any{
				"name":   nodeName,
				"type":   node.Type,
				"status": "error",
				"error":  err.Error(),
			})
			continue
		}

		// Capture the (post-defaults, post-auto-allocation) ports in state
		// so inspect / health / diagnose / events can target the right
		// host endpoint without re-reading the intent file.
		mn := state.ManagedNode{
			Name:    nodeName,
			Version: node.Version,
			Network: parsed.Network,
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
			Labels:      node.Labels,
		}
		store.UpsertNode(deployState, mn)

		deployed = append(deployed, map[string]any{
			"name":   nodeName,
			"type":   node.Type,
			"status": "running",
			"endpoints": map[string]string{
				"http": fmt.Sprintf("http://127.0.0.1:%d", node.Ports.HTTP),
				"grpc": fmt.Sprintf("127.0.0.1:%d", node.Ports.GRPC),
			},
		})
	}

	result := map[string]any{
		"network": parsed.Name,
		"nodes":   deployed,
	}

	// Deploy monitoring stack only when --monitor flag is explicitly passed.
	if createMonitor {
		if parsed.Monitoring == nil {
			parsed.Monitoring = &intent.Monitoring{}
		}
		parsed.Monitoring.Enabled = intent.BoolPtr(true)
		intent.ApplyMonitoringDefaults(parsed.Monitoring)

		monResult := deployNetworkMonitoring(cmd.Context(), tgt, workDir, parsed)
		if monResult.error != "" {
			result["monitoring_error"] = monResult.error
		} else {
			result["monitoring"] = monResult.urls
		}
	}

	if err := store.Save(deployState); err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError,
			"failed to persist state: "+err.Error())
	}
	writeAudit(auditEvent{
		Command: "network-create",
		Node:    parsed.Name,
		Target:  parsed.Target.Type,
		Result:  "success",
		Start:   start,
	})

	output.WriteJSON(os.Stdout, result)
	return nil
}

// autoWireActivePeers fills each node's network_overrides.active_peers
// with the addresses of all OTHER nodes in the network. We only touch
// nodes whose active_peers is unset; the user can opt out by supplying
// even an empty list ([] explicitly, parsed as a non-nil zero-length
// slice). Addresses use the docker-compose container name as host so
// they resolve correctly inside the shared docker network.
//
// Why this is necessary: with auto_ports the rendered P2P port is no
// longer 18888, and the user can't know that port at intent-write time.
// seed.node lists alone aren't enough to keep peers connected when
// node.discovery is off, so node.active is the right field — and
// network create is the one command that knows enough about siblings
// to populate it deterministically.
func autoWireActivePeers(parsed *intent.Intent) {
	addresses := make([]string, len(parsed.Nodes))
	for i := range parsed.Nodes {
		nodeName := fmt.Sprintf("%s-node%d", parsed.Name, i)
		addresses[i] = fmt.Sprintf("%s:%d", nodeName, parsed.Nodes[i].Ports.P2P)
	}

	for i := range parsed.Nodes {
		// Skip nodes the user explicitly configured (even with []).
		if parsed.Nodes[i].NetworkOverrides.ActivePeers != nil {
			continue
		}
		var others []string
		for j, addr := range addresses {
			if j == i {
				continue
			}
			others = append(others, addr)
		}
		if len(others) == 0 {
			continue
		}
		parsed.Nodes[i].NetworkOverrides.ActivePeers = &others
	}
}

func findTemplatesDir() string {
	if d := os.Getenv("TROND_TEMPLATES_DIR"); d != "" {
		return d
	}
	candidates := []string{"templates", "./templates"}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(c, "main_net_config.conf")); err == nil {
				return c
			}
		}
	}
	return ""
}

// deployNetworkMonitoring deploys a single monitoring stack for an entire
// private network. All nodes are scraped via the shared docker network.
func deployNetworkMonitoring(ctx context.Context, tgt target.Target, workDir string, parsed *intent.Intent) monitoringResult {
	var targets []render.MonitoringTarget
	for i, node := range parsed.Nodes {
		nodeName := fmt.Sprintf("%s-node%d", parsed.Name, i)
		targets = append(targets, render.MonitoringTarget{
			Name:    nodeName,
			Address: fmt.Sprintf("%s:%d", nodeName, node.Ports.Metrics),
			Labels: map[string]string{
				"group":    "group-tron",
				"instance": nodeName,
				"network":  parsed.Network,
				"type":     node.Type,
			},
		})
	}

	networkName := "trond-" + parsed.Name
	composeData := render.RenderMonitoringCompose(parsed.Name, parsed, targets, networkName)
	promConfig := render.RenderPrometheusConfig(targets, parsed.Monitoring.Prometheus.Retention)
	dsYAML, provYAML := render.RenderGrafanaProvisioning(
		fmt.Sprintf("http://prometheus:%d", parsed.Monitoring.Prometheus.Port),
	)

	dashboards := make(map[string][]byte)
	for _, name := range render.DashboardNames() {
		data, err := render.LoadDashboard(name)
		if err != nil {
			continue
		}
		dashboards[name] = render.NormalizeDashboard(data)
	}

	monOpts := runtime.MonitoringDeployOpts{
		Name:              parsed.Name,
		ComposeData:       []byte(composeData),
		PrometheusConfig:  []byte(promConfig),
		GrafanaDatasource: []byte(dsYAML),
		GrafanaProvider:   []byte(provYAML),
		Dashboards:        dashboards,
	}

	rt := runtime.NewMonitoringRuntime(tgt, workDir)
	if err := rt.Deploy(ctx, monOpts); err != nil {
		return monitoringResult{error: err.Error()}
	}

	urls := map[string]string{
		"grafana_url": fmt.Sprintf("http://127.0.0.1:%d", parsed.Monitoring.Grafana.Port),
	}
	if parsed.Monitoring.Prometheus.Port > 0 {
		urls["prometheus_url"] = fmt.Sprintf("http://127.0.0.1:%d", parsed.Monitoring.Prometheus.Port)
	}
	return monitoringResult{
		urls: urls,
	}
}

// monitoringResult is shared with cmd/apply.go's monitoring result.
type monitoringResult struct {
	error string
	urls  map[string]string
}

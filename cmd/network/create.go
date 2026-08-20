package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/apply"
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

	// Every node goes through internal/apply.Apply, the same core `trond
	// apply` uses. This loop used to hand-roll render + Deploy + state,
	// which quietly diverged from Apply in three ways: it never called
	// internal/build (so a node with `build:` rendered an empty image:
	// and deployed nothing usable, with no error), it hardcoded JDK 17
	// for JVM arg selection instead of probing the target, and it
	// hardcoded the docker runtime instead of honouring target.runtime.
	for i := range parsed.Nodes {
		sub, nodeName, intentHash, err := nodeIntent(parsed, i)
		if err != nil {
			return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
		}
		res, err := apply.Apply(cmd.Context(), apply.Options{
			Intent:         sub,
			Target:         tgt,
			Store:          store,
			State:          deployState,
			IntentHash:     intentHash,
			Existing:       store.GetNode(deployState, nodeName),
			TemplateDir:    templateDir,
			DeploymentsDir: workDir,
			IntentPath:     createIntentPath,
			// Now that create is an Apply caller it inherits the core's
			// state-based --require-private gate, same as cmd/apply.go.
			// guard.Enforce above only sees the intent's network LABEL;
			// this also checks the network recorded in state for a node
			// already deployed under the same name (#203).
			RequirePrivate: guard.Requested(),
			// The network owns one monitoring stack covering every node
			// (deployNetworkMonitoring below). Intent.Monitoring stays set
			// so RenderHOCON still auto-enables the node's metrics port.
			SkipMonitoring: true,
		})
		if err != nil {
			deployed = append(deployed, map[string]any{
				"name":   nodeName,
				"type":   parsed.Nodes[i].Type,
				"status": "error",
				"error":  err.Error(),
			})
			continue
		}

		entry := map[string]any{
			"name":      nodeName,
			"type":      parsed.Nodes[i].Type,
			"status":    "running",
			"outcome":   res.Outcome,
			"endpoints": res.Endpoints,
		}
		if res.Build != nil {
			entry["build"] = res.Build
		}
		deployed = append(deployed, entry)
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
		"grafana_url": fmt.Sprintf("http://%s:%d", target.EndpointHost(parsed.Target.Type, parsed.Target.Host), parsed.Monitoring.Grafana.Port),
	}
	if parsed.Monitoring.Prometheus.Port > 0 {
		urls["prometheus_url"] = fmt.Sprintf("http://%s:%d", target.EndpointHost(parsed.Target.Type, parsed.Target.Host), parsed.Monitoring.Prometheus.Port)
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

// nodeIntent projects the multi-node network intent down to the
// single-node intent apply.Apply consumes, returning it alongside the
// node's deployed name and its own intent hash.
//
// Apply keys everything off Intent.Name — the compose project, the state
// entry, Result.Name — and reads only Intent.Nodes[0], so the projection
// has to rename as well as slice. Everything else (target, network,
// monitoring, template dir) is shared and copied through unchanged.
//
// The hash is computed over the projected intent rather than over the
// network intent file, so idempotency is per node: editing one node's
// ports must redeploy that node and leave its siblings at no_change.
func nodeIntent(parsed *intent.Intent, i int) (sub *intent.Intent, name, hash string, err error) {
	if i < 0 || i >= len(parsed.Nodes) {
		return nil, "", "", fmt.Errorf("node index %d out of range (%d nodes)", i, len(parsed.Nodes))
	}
	name = fmt.Sprintf("%s-node%d", parsed.Name, i)

	// Shallow struct copy is deliberate: the shared fields (Target,
	// Monitoring, ...) are read-only from here on, and Apply must see the
	// same values every node saw. Only Name and Nodes are re-pointed.
	clone := *parsed
	clone.Name = name
	clone.Nodes = []intent.NodeSpec{parsed.Nodes[i]}

	data, err := yaml.Marshal(&clone)
	if err != nil {
		return nil, "", "", fmt.Errorf("hash node %d intent: %w", i, err)
	}
	return &clone, name, apply.IntentHashFromBytes(data), nil
}

package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// sortedKeys returns the keys of m sorted lexicographically. We use it for
// every map → YAML emission so the output is deterministic (intent_hash and
// config_hash compare cleanly across runs).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// composeEnvLines produces the "KEY=VAL" entries for a service's
// "environment:" block.
//
// The witness private key is inlined directly into the rendered HOCON
// (java-tron's typesafe-config does NOT do ${ENV} substitution) so it
// does NOT leak into the container env. The keystore password, however,
// is read by the runtime startup hook, so we still pass it through when
// witness_key.keystore_password_env is set.
func composeEnvLines(node *intent.NodeSpec) []string {
	env := make(map[string]string, len(node.ExtraEnv)+1)
	for key, value := range node.ExtraEnv {
		env[key] = strings.ReplaceAll(value, "$", "$$")
	}
	if node.Type == "witness" && node.WitnessKey != nil && node.WitnessKey.KeystorePasswordEnv != "" {
		passwordEnv := node.WitnessKey.KeystorePasswordEnv
		env[passwordEnv] = "${" + passwordEnv + "}"
	}
	if len(env) == 0 {
		return nil
	}
	keys := sortedKeys(env)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Filesystem layout inside the official tronprotocol/java-tron image
// (defined by tron-docker tools/docker/Dockerfile + docker-entrypoint.sh):
//
//	/java-tron/                 — WORKDIR
//	/java-tron/bin/FullNode     — entrypoint binary
//	/java-tron/conf/            — host-mounted config dir (we drop the rendered
//	                              <name>_config.conf here)
//	/java-tron/output-directory — chain DB / state (must be persisted)
//	/java-tron/logs             — gc.log, tron.log (worth persisting for triage)
//
// Renderings before this aligned with neither the image nor the entrypoint:
// they pointed at /usr/local/tron/FullNode.jar (does not exist), mounted a
// /data volume the runtime never wrote to, and bypassed the entrypoint
// (./bin/docker-entrypoint.sh) by spelling out `java -jar`. Containers
// silently restart-looped as a result. This file is the corrected layout.
const (
	ContainerWorkdir   = "/java-tron"
	ContainerDataDir   = ContainerWorkdir + "/output-directory"
	ContainerConfDir   = ContainerWorkdir + "/conf"
	ContainerLogPath   = ContainerWorkdir + "/logs/tron.log"
	containerConfigDir = ContainerConfDir
	containerDataDir   = ContainerDataDir
	containerLogDir    = ContainerWorkdir + "/logs"
)

// RenderCompose generates a docker-compose.yaml from the intent.
//
// name is the deployment-unique identifier used for the service, container,
// volume, and config-file basename. Single-node deploys pass intent.Name;
// `network create` passes the per-node "<network>-node<i>" so multi-node
// networks don't collide on container_name.
//
// imageOverride, when non-empty, replaces the image field (and suppresses
// the version-suffix logic). Used by the build pipeline (Phase 3) when
// `build.artifact: image` produces a locally-tagged image — the
// generated compose then references that local tag and the renderer
// also emits `pull_policy: never` so compose does not try to fetch
// the tag from a remote registry.
func RenderCompose(name string, i *intent.Intent, node *intent.NodeSpec, configPath string, jvmArgs string, imageOverride string) string {
	if name == "" {
		name = i.Name
	}

	var image string
	switch {
	case imageOverride != "":
		image = imageOverride
	case node.Version != "" && node.Version != "latest":
		image = fmt.Sprintf("%s:%s", node.Image, node.Version)
	default:
		image = node.Image
	}

	ports := renderComposePorts(node)
	memory := node.Resources.Memory
	if memory == "" {
		memory = "16GB"
	}
	// Convert memory format: "16GB" → "16g"
	memoryLimit := strings.ToLower(strings.ReplaceAll(memory, "B", ""))

	containerConfigPath := fmt.Sprintf("%s/%s.conf", containerConfigDir, name)

	restartPolicy := node.Restart
	if restartPolicy == "" {
		restartPolicy = "unless-stopped"
	}

	var sb strings.Builder
	sb.WriteString("services:\n")
	sb.WriteString(fmt.Sprintf("  %s:\n", name))
	sb.WriteString(fmt.Sprintf("    image: %s\n", image))
	if imageOverride != "" {
		// Locally-built image; compose 3.9+ honors pull_policy.
		// Without this, `docker compose up` would try to pull the
		// tag from docker hub and either fail (no remote) or pull
		// an unrelated upstream image.
		sb.WriteString("    pull_policy: never\n")
	}
	sb.WriteString(fmt.Sprintf("    container_name: %s\n", name))
	sb.WriteString(fmt.Sprintf("    restart: %s\n", restartPolicy))
	if len(node.Labels) > 0 {
		sb.WriteString("    labels:\n")
		for _, k := range sortedKeys(node.Labels) {
			sb.WriteString(fmt.Sprintf("      %s: %q\n", k, node.Labels[k]))
		}
	}
	if len(node.DependsOn) > 0 {
		sb.WriteString("    depends_on:\n")
		for _, dep := range node.DependsOn {
			sb.WriteString(fmt.Sprintf("      - %s\n", dep))
		}
	}
	if len(node.Networks) > 0 {
		sb.WriteString("    networks:\n")
		for _, n := range node.Networks {
			sb.WriteString(fmt.Sprintf("      - %s\n", n))
		}
	}
	if len(node.Entrypoint) > 0 {
		sb.WriteString("    entrypoint:\n")
		for _, e := range node.Entrypoint {
			sb.WriteString(fmt.Sprintf("      - %q\n", e))
		}
	}
	if node.ShmSize != "" {
		sb.WriteString(fmt.Sprintf("    shm_size: %q\n", node.ShmSize))
	}
	if node.Ulimits != nil && node.Ulimits.NOFile > 0 {
		sb.WriteString("    ulimits:\n")
		sb.WriteString(fmt.Sprintf("      nofile: %d\n", node.Ulimits.NOFile))
	}
	if len(node.ExtraHosts) > 0 {
		sb.WriteString("    extra_hosts:\n")
		for _, k := range sortedKeys(node.ExtraHosts) {
			sb.WriteString(fmt.Sprintf("      - %s:%s\n", k, node.ExtraHosts[k]))
		}
	}
	if hc := node.Healthcheck; hc != nil {
		sb.WriteString("    healthcheck:\n")
		sb.WriteString("      test:\n")
		for _, t := range hc.Test {
			sb.WriteString(fmt.Sprintf("        - %q\n", t))
		}
		if hc.Interval != "" {
			sb.WriteString(fmt.Sprintf("      interval: %s\n", hc.Interval))
		}
		if hc.Timeout != "" {
			sb.WriteString(fmt.Sprintf("      timeout: %s\n", hc.Timeout))
		}
		if hc.Retries > 0 {
			sb.WriteString(fmt.Sprintf("      retries: %d\n", hc.Retries))
		}
		if hc.StartPeriod != "" {
			sb.WriteString(fmt.Sprintf("      start_period: %s\n", hc.StartPeriod))
		}
	}
	if log := node.Logging; log != nil && log.Driver != "" {
		sb.WriteString("    logging:\n")
		sb.WriteString(fmt.Sprintf("      driver: %s\n", log.Driver))
		if len(log.Options) > 0 {
			sb.WriteString("      options:\n")
			for _, k := range sortedKeys(log.Options) {
				sb.WriteString(fmt.Sprintf("        %s: %q\n", k, log.Options[k]))
			}
		}
	}
	sb.WriteString("    ports:\n")
	for _, p := range ports {
		sb.WriteString(fmt.Sprintf("      - %q\n", p))
	}
	sb.WriteString("    volumes:\n")
	// HOCON config: read-only bind mount of the rendered file into /java-tron/conf.
	sb.WriteString(fmt.Sprintf("      - ./%s.conf:%s:ro\n", name, containerConfigPath))
	// Persistent state and logs — sources resolve from intent.storage with
	// per-node defaults of "<name>-data" / "<name>-logs" (named volumes).
	dataSrc := storageSource(name, &node.Storage, "data")
	logsSrc := storageSource(name, &node.Storage, "logs")
	sb.WriteString(fmt.Sprintf("      - %s:%s\n", dataSrc, containerDataDir))
	sb.WriteString(fmt.Sprintf("      - %s:%s\n", logsSrc, containerLogDir))

	// Environment merges (a) automatic witness-key passthrough, kept
	// separate from user-controllable fields so it can't be omitted by
	// accident, and (b) intent.extra_env for arbitrary per-deploy values.
	envLines := composeEnvLines(node)
	if len(envLines) > 0 {
		sb.WriteString("    environment:\n")
		for _, line := range envLines {
			sb.WriteString("      - " + line + "\n")
		}
	}

	sb.WriteString("    deploy:\n")
	sb.WriteString("      resources:\n")
	sb.WriteString("        limits:\n")
	sb.WriteString(fmt.Sprintf("          memory: %s\n", memoryLimit))
	if node.Resources.CPU != "" {
		sb.WriteString(fmt.Sprintf("          cpus: %q\n", node.Resources.CPU))
	}

	// Command goes to ./bin/docker-entrypoint.sh which prefixes
	// ./bin/FullNode and passes the rest. We let the entrypoint do that
	// rather than re-implementing it here.
	sb.WriteString("    command:\n")
	if jvmArgs != "" {
		// FullNode accepts JVM args via "-jvm <quoted single string>".
		sb.WriteString("      - \"-jvm\"\n")
		sb.WriteString(fmt.Sprintf("      - %q\n", "{"+jvmArgs+"}"))
	}
	sb.WriteString("      - \"-c\"\n")
	sb.WriteString(fmt.Sprintf("      - %q\n", containerConfigPath))
	// Extra args slot in between the standard FullNode flags and --witness
	// so the witness flag (when present) always lands last.
	for _, a := range node.ExtraArgs {
		sb.WriteString(fmt.Sprintf("      - %q\n", a))
	}
	if node.Type == "witness" {
		sb.WriteString("      - \"--witness\"\n")
	}

	// Top-level "volumes:" only declares named volumes; bind-mount paths
	// (those starting with "/") need no declaration.
	dataDecl := volumeDeclName(dataSrc)
	logsDecl := volumeDeclName(logsSrc)
	if dataDecl != "" || logsDecl != "" {
		sb.WriteString("\n")
		sb.WriteString("volumes:\n")
		if dataDecl != "" {
			sb.WriteString(fmt.Sprintf("  %s:\n", dataDecl))
		}
		if logsDecl != "" {
			sb.WriteString(fmt.Sprintf("  %s:\n", logsDecl))
		}
	}

	// Top-level "networks:" declares each user-supplied network as
	// external, expecting the operator (or the test harness) to have
	// created it ahead of time. Auto-creating would mask typos.
	if len(node.Networks) > 0 {
		sb.WriteString("\n")
		sb.WriteString("networks:\n")
		for _, n := range node.Networks {
			sb.WriteString(fmt.Sprintf("  %s:\n", n))
			sb.WriteString("    external: true\n")
		}
	}

	return sb.String()
}

// storageSource returns the compose volume "source" string for one of the
// two storage roles (data, logs). Resolution order:
//
//  1. explicit storage.data / storage.logs in intent ⇒ used as-is
//  2. storage.path set ⇒ "<path>/<role>" (bind-mount under a single root)
//  3. default ⇒ "<name>-<role>" named volume
//
// A leading "/" marks a bind path; anything else is a named volume.
func storageSource(name string, s *intent.Storage, role string) string {
	switch role {
	case "data":
		if s != nil && s.Data != "" {
			return s.Data
		}
	case "logs":
		if s != nil && s.Logs != "" {
			return s.Logs
		}
	}
	if s != nil && s.StoragePath != "" {
		// Trim trailing slash for clean joining.
		root := strings.TrimRight(s.StoragePath, "/")
		return root + "/" + role
	}
	return name + "-" + role
}

// volumeDeclName returns the named-volume identifier that should appear in
// the top-level "volumes:" block, or "" when the source is a bind path
// (which docker-compose mounts without prior declaration).
func volumeDeclName(src string) string {
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") {
		return ""
	}
	return src
}

// renderComposePorts produces the host:container port mappings.
//
// The P2P port also needs UDP exposed for kad-style peer discovery. HTTP /
// gRPC are TCP-only. JSON-RPC and metrics are published when enabled.
// Solidity HTTP/gRPC ports remain internal to the node and are intentionally
// not published on the host; their persisted state ports support node probes,
// not external exposure.
func renderComposePorts(node *intent.NodeSpec) []string {
	ports := []string{
		fmt.Sprintf("%d:%d", node.Ports.HTTP, node.Ports.HTTP),
		fmt.Sprintf("%d:%d", node.Ports.GRPC, node.Ports.GRPC),
		fmt.Sprintf("%d:%d", node.Ports.P2P, node.Ports.P2P),
		fmt.Sprintf("%d:%d/udp", node.Ports.P2P, node.Ports.P2P),
	}

	if node.Features.JSONRPC != nil && *node.Features.JSONRPC {
		ports = append(ports, fmt.Sprintf("%d:%d", node.Ports.JSONRPC, node.Ports.JSONRPC))
	}

	if node.Features.Metrics != nil && *node.Features.Metrics {
		ports = append(ports, fmt.Sprintf("%d:%d", node.Ports.Metrics, node.Ports.Metrics))
	}
	return ports
}

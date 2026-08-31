package intent

import (
	"fmt"
	"net"
	"runtime"
)

// DefaultRuntime is the single source of truth for the runtime
// default rule. ApplyDefaults uses it to fill intent.Target.Runtime;
// intent.Validate uses it to derive the would-be effective runtime
// when checking the build artifact/runtime mutex; apply.Apply uses
// it as a last-resort fallback for programmatic callers that
// bypass ApplyDefaults. Three sites, one rule.
//
// Rules (post-Phase 3):
//
//  1. Intents with a `build:` block default the runtime to match
//     the build artifact:
//     artifact: jar    → runtime: jar
//     artifact: image  → runtime: docker
//     (Artifact itself defaults to "jar" if unspecified — same as
//     applyNodeDefaults — so a bare `build: { source: ... }` still
//     ends up jar/jar.)
//  2. Intents without any `build:` block keep the legacy "docker"
//     default unchanged.
func DefaultRuntime(intent *Intent) string {
	for i := range intent.Nodes {
		if intent.Nodes[i].Build == nil {
			continue
		}
		artifact := intent.Nodes[i].Build.Artifact
		if artifact == "" {
			artifact = "jar"
		}
		switch artifact {
		case "image":
			return "docker"
		default:
			return "jar"
		}
	}
	return "docker"
}

// DefaultPlatform returns the docker `--platform` string for the
// trond binary's host architecture, used as the default for
// build.platform when the user hasn't specified one. Mirrors
// Go's runtime.GOARCH → docker convention:
//
//	amd64 / 386       → linux/amd64
//	arm64             → linux/arm64
//	other (ppc64le…)  → linux/amd64 (most-likely supported)
func DefaultPlatform() string {
	switch runtime.GOARCH {
	case "arm64":
		return "linux/arm64"
	default:
		return "linux/amd64"
	}
}

// DefaultJDKForPlatform returns the java-tron-supported JDK version
// for the given docker platform string. Per upstream java-tron's
// compatibility matrix:
//
//	linux/amd64 → JDK 8  (only legacy-tested combo on Intel)
//	linux/arm64 → JDK 17 (only version with mature arm64 JIT + the
//	                      tested compat matrix)
//
// Users override via `build.jdk:` explicitly; this default is what
// trond picks when the field is empty.
func DefaultJDKForPlatform(platform string) string {
	switch platform {
	case "linux/arm64":
		return "17"
	default:
		return "8"
	}
}

// ApplyDefaults fills in default values for fields not explicitly set in the intent.
func ApplyDefaults(intent *Intent) {
	// Target defaults
	if intent.Target.Port == 0 && intent.Target.Type == "ssh" {
		intent.Target.Port = 22
	}
	if intent.Target.Runtime == "" {
		// Source of truth in DefaultRuntime; both intent.Validate and
		// apply.Apply also consult that helper so the rule lives in
		// exactly one place.
		intent.Target.Runtime = DefaultRuntime(intent)
	}
	if intent.Target.IdentityFile == "" && intent.Target.Type == "ssh" {
		intent.Target.IdentityFile = "~/.ssh/id_rsa"
	}

	for i := range intent.Nodes {
		applyNodeDefaults(&intent.Nodes[i])
	}

	ApplyMonitoringDefaults(intent.Monitoring)

	if intent.Target.AutoPorts {
		// Replace every port that's currently at its java-tron default with
		// a free OS-assigned port. Errors here are non-fatal — leaving the
		// default port is the same behavior the user gets without auto.
		_ = allocateFreePorts(intent)
	}
}

// allocateFreePorts walks each node's PortMapping and replaces the standard
// java-tron defaults with OS-assigned free ports. Any port the user set
// explicitly to a non-default value is preserved.
func allocateFreePorts(intent *Intent) error {
	defaults := map[string]int{
		"http":          8090,
		"grpc":          50051,
		"solidity_http": 8091,
		"solidity_grpc": 50061,
		"jsonrpc":       8545,
		"p2p":           18888,
		"metrics":       9527,
	}
	used := make(map[int]bool)

	// pickPort returns a port that is free for BOTH TCP and UDP. The P2P
	// port needs both (java-tron's discovery is UDP), and docker compose
	// fails the whole container when either family can't bind. We don't
	// know up front which port is destined for P2P vs HTTP, so we apply
	// the strict TCP+UDP test to every allocation — slightly wasteful for
	// HTTP-only ports, but never wrong.
	pickPort := func() (int, error) {
		for attempt := 0; attempt < 32; attempt++ {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return 0, fmt.Errorf("listen tcp: %w", err)
			}
			port := l.Addr().(*net.TCPAddr).Port
			l.Close()
			if used[port] {
				continue
			}
			// Verify the same port is free on UDP. Failure means some
			// other process holds the udp socket — try another port.
			udp, uerr := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			if uerr != nil {
				continue
			}
			udp.Close()
			used[port] = true
			return port, nil
		}
		return 0, fmt.Errorf("could not find a free TCP+UDP port after 32 attempts")
	}

	for i := range intent.Nodes {
		p := &intent.Nodes[i].Ports
		if p.HTTP == defaults["http"] {
			if port, err := pickPort(); err == nil {
				p.HTTP = port
			}
		}
		if p.GRPC == defaults["grpc"] {
			if port, err := pickPort(); err == nil {
				p.GRPC = port
			}
		}
		if p.SolidityHTTP == defaults["solidity_http"] {
			if port, err := pickPort(); err == nil {
				p.SolidityHTTP = port
			}
		}
		if p.SolidityGRPC == defaults["solidity_grpc"] {
			if port, err := pickPort(); err == nil {
				p.SolidityGRPC = port
			}
		}
		if p.JSONRPC == defaults["jsonrpc"] {
			if port, err := pickPort(); err == nil {
				p.JSONRPC = port
			}
		}
		if p.P2P == defaults["p2p"] {
			if port, err := pickPort(); err == nil {
				p.P2P = port
			}
		}
		if p.Metrics == defaults["metrics"] {
			if port, err := pickPort(); err == nil {
				p.Metrics = port
			}
		}
	}
	return nil
}

func applyNodeDefaults(node *NodeSpec) {
	if node.Version == "" {
		node.Version = "latest"
	}
	// Skip the default Image when a Build or Jar block is present — they're
	// mutually exclusive (FR-005). Otherwise the mutex check would
	// fail on re-validation post-defaults, and worse: a docker
	// runtime would try to pull an image trond doesn't intend to use.
	if node.Image == "" && node.Build == nil && node.Jar == nil {
		node.Image = "tronprotocol/java-tron"
	}
	if node.InstallPath == "" {
		node.InstallPath = "/opt/tron"
	}
	if node.ProcessManager == "" {
		node.ProcessManager = "systemd"
	}
	if node.SystemUser == "" {
		node.SystemUser = "tron"
	}

	// BuildSpec defaults. The build pipeline owns the canonical
	// defaults (build.Request.withDefaults), but filling them here too
	// makes `trond config validate --explain` surface them and keeps
	// downstream consumers from having to re-derive the same values.
	// Keep the two in lockstep.
	if node.Build != nil {
		if node.Build.Revision == "" {
			node.Build.Revision = "HEAD"
		}
		// Platform defaults to host arch; JDK then defaults based on
		// the (possibly-overridden) platform per java-tron's compat
		// matrix. The order matters: if a user wrote platform but
		// not JDK, we pick the matching JDK; if user wrote JDK but
		// not platform, we pick host platform and don't second-guess
		// their JDK.
		if node.Build.Platform == "" {
			node.Build.Platform = DefaultPlatform()
		}
		if node.Build.JDK == "" {
			node.Build.JDK = DefaultJDKForPlatform(node.Build.Platform)
		}
		if node.Build.Artifact == "" {
			node.Build.Artifact = "jar"
		}
		if node.Build.Builder == "" {
			node.Build.Builder = "docker"
		}
		if node.Build.GradleTask == "" {
			switch node.Build.Artifact {
			case "jar":
				node.Build.GradleTask = "shadowJar"
			case "image":
				node.Build.GradleTask = "dockerBuild"
			}
		}
		// image_strategy defaults to "gradle" for backward compat
		// with Phase 3. Users on stock java-tron set
		// `image_strategy: jar-wrap` explicitly — trond then runs
		// the Phase 5d path which produces the image from a
		// JAR-wrap Dockerfile and ignores GradleTask's dockerBuild
		// default. We do NOT auto-detect strategy: requiring
		// explicit opt-in keeps existing tron-docker-style intents
		// working unchanged.
		if node.Build.Artifact == "image" && node.Build.ImageStrategy == "" {
			node.Build.ImageStrategy = "gradle"
		}
	}

	// Feature defaults
	if node.Features.RateLimit == nil {
		node.Features.RateLimit = BoolPtr(true)
	}

	// Resource defaults
	if node.Resources.Memory == "" {
		node.Resources.Memory = "16GB"
	}

	// Port defaults
	if node.Ports.HTTP == 0 {
		node.Ports.HTTP = 8090
	}
	if node.Ports.GRPC == 0 {
		node.Ports.GRPC = 50051
	}
	if node.Ports.SolidityHTTP == 0 {
		node.Ports.SolidityHTTP = 8091
	}
	if node.Ports.SolidityGRPC == 0 {
		node.Ports.SolidityGRPC = 50061
	}
	if node.Ports.JSONRPC == 0 {
		node.Ports.JSONRPC = 8545
	}
	if node.Ports.P2P == 0 {
		node.Ports.P2P = 18888
	}
	if node.Ports.Metrics == 0 {
		node.Ports.Metrics = 9527
	}

	// JVM defaults. GC and GC logging are opt-in: emitting them by default
	// collides with the official java-tron image's start.sh ("Multiple
	// garbage collectors selected") and triggers a container restart loop.
	// Users who want trond-managed GC tuning must set jvm.gc explicitly.
	if node.JVM == nil {
		node.JVM = &JVMConfig{}
	}
	if node.JVM.GC == "" {
		node.JVM.GC = "auto"
	}
}

// ApplyMonitoringDefaults fills in default values for monitoring fields.
func ApplyMonitoringDefaults(m *Monitoring) {
	if m == nil {
		return
	}
	if m.Enabled == nil {
		m.Enabled = BoolPtr(false)
	}
	if m.Prometheus.Port == 0 {
		// Default to 0 (unexposed). Prometheus is reachable inside the
		// docker network by Grafana; external access is opt-in via an
		// explicit port in the intent.
		m.Prometheus.Port = 0
	}
	if m.Grafana.Port == 0 {
		m.Grafana.Port = 3000
	}
	if m.Prometheus.Retention == "" {
		m.Prometheus.Retention = "7d"
	}
}

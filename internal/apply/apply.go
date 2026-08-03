// Package apply runs the deploy phase of `trond apply` as a pure
// function so callers other than cobra (MCP server, recipe runner,
// e2e test harness) can drive it without forking a subprocess.
//
// Scope: this package owns steps 8-10 of the apply pipeline as
// originally documented in cmd/apply.go::runApply:
//
//  8. Render HOCON + compose / systemd, derive JVM args
//  9. Hand off to docker / jar runtime, persist node into state
//  10. Optionally block on the node's HTTP API readiness (--wait)
//
// What it does NOT own — these stay in cmd/apply.go because they're
// either presentation-layer or require interactive context the
// callers control:
//
//   - Parsing flags / loading the intent file (caller responsibility)
//   - Resolving target.Target (cmd needs SSH cert handling, MCP gets
//     its target from a fresh resolveTarget call)
//   - Acquiring the state lock (cmd's defer pattern; MCP holds it
//     for the duration of the tool call)
//   - The HUMAN_REQUIRED gate for changed intents (this is a
//     human-confirmation policy; callers decide when it fires)
//   - Audit log writes (cmd-only — recipe + MCP have their own
//     audit story via stdout JSON capture)
//
// In short: prepare the inputs (Intent + Target + Store + State +
// IntentHash), call Apply, format the Result however the caller
// wants. Apply itself is a pure function modulo the file-system
// side effects of rendering + deploying.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// Options carries everything Apply needs that the caller is best
// positioned to assemble (intent, target, store, state, hash).
type Options struct {
	Intent     *intent.Intent
	Target     target.Target
	Store      *state.Store
	State      *state.DeploymentState
	IntentHash string

	// Existing is store.GetNode(state, intent.Name) at call time, or
	// nil when there's no prior managed node by that name. Carrying
	// it here avoids a redundant lookup inside Apply.
	Existing *state.ManagedNode

	// TemplateDir is the optional override for the embedded HOCON
	// template tree. Empty string uses the embedded copy (the common
	// case for production builds).
	TemplateDir string

	// DeploymentsDir is the on-disk root the docker runtime writes
	// rendered compose + .conf files under (typically ~/.trond/deployments).
	DeploymentsDir string

	// JDKVersion is honored if non-zero (lets callers skip the
	// `java -version` probe for known targets). 0 triggers detection.
	JDKVersion int

	// EnvVars are the Witness-key-style env vars that get passed
	// through to the docker container / systemd unit.
	EnvVars map[string]string

	// IntentPath is the on-disk path of the intent.yaml that produced
	// Intent. Used to resolve `build.source: ./relative/path` against
	// the intent file's directory per spec/002 FR-021. Optional when
	// the intent has no `build:` block.
	IntentPath string

	// Wait + WaitTimeout: when Wait is true, Apply blocks until the
	// node's HTTP API responds 2xx or WaitTimeout elapses. Wait
	// failures don't roll the deploy back; they surface in
	// Result.WaitError / Result.Ready.
	Wait        bool
	WaitTimeout time.Duration

	// RequirePrivate is the machine-enforced safety gate: when true,
	// Apply refuses any non-private intent before doing anything. Lives
	// in the core (not just cmd) so EVERY caller — CLI apply, network
	// create, MCP — inherits the guarantee and it can't be bypassed by
	// choosing a different entry point. Pure opt-in; callers set it.
	RequirePrivate bool
}

// Result is the structured output of one Apply call. Stable JSON
// shape (matches schemas/output/apply.schema.json) so MCP / recipe /
// CLI presentations are interchangeable.
type Result struct {
	Name       string `json:"name"`
	Outcome    string `json:"outcome"` // created | updated | no_change
	IntentHash string `json:"intent_hash"`
	ConfigHash string `json:"config_hash"`
	Version    string `json:"version"`
	Runtime    string `json:"runtime"`
	// Network + IsPrivate echo the deployed network and whether it is a
	// private (agent-safe-to-mutate) net, so a caller sees the same
	// safety fact `status --json` exposes without a second call.
	Network    string            `json:"network"`
	IsPrivate  bool              `json:"is_private"`
	Endpoints  map[string]string `json:"endpoints"`
	DurationMs int64             `json:"duration_ms"`

	// Ready / WaitedMs / WaitError are only set when Options.Wait was true.
	Ready     *bool  `json:"ready,omitempty"`
	WaitedMs  int64  `json:"waited_ms,omitempty"`
	WaitError string `json:"wait_error,omitempty"`

	// MonitoringError is set when the monitoring stack deployment failed.
	// The node itself deployed successfully; this is a non-fatal warning.
	MonitoringError string `json:"monitoring_error,omitempty"`

	// MonitoringEndpoints exposes the Prometheus + Grafana URLs when
	// monitoring was successfully deployed.
	MonitoringEndpoints map[string]string `json:"monitoring,omitempty"`

	// Build is populated when the intent carried a `build:` block.
	// Matches the build.Result JSON shape (schemas/output/build.schema.json).
	// Omitted from the result envelope when no build was needed
	// (image or pre-built jar path).
	Build *BuildSummary `json:"build,omitempty"`
}

// BuildSummary is the slice of build.Result the apply envelope
// surfaces. The full per-build manifest stays under
// ~/.trond/builds/manifest/<key>.json; this is what an agent sees
// inline with `trond apply -o json`.
type BuildSummary struct {
	CacheKey       string `json:"cache_key"`
	SourceRevision string `json:"source_revision"`
	Dirty          bool   `json:"dirty"`
	ArtifactPath   string `json:"artifact_path,omitempty"`
	ImageTag       string `json:"image_tag,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	// BuilderImage records which pinned JDK builder produced this
	// artifact (e.g. eclipse-temurin:8-jdk-jammy@sha256:...). Lets an
	// agent answer "what image built this?" without round-tripping
	// through `trond build inspect`.
	BuilderImage string `json:"builder_image,omitempty"`
	// Platform + JDKVersion let an agent answer "is this the amd64
	// JDK 8 build or the arm64 JDK 17 build?" inline. Both come from
	// the build.Result manifest.
	Platform   string `json:"platform,omitempty"`
	JDKVersion string `json:"jdk_version,omitempty"`
	CacheHit   bool   `json:"cache_hit"`
	DurationMs int64  `json:"duration_ms"`
}

// Apply runs the deploy phase. Returns a Result on success or
// partial-success (deploy succeeded, wait timed out); returns an error
// only when the deploy itself failed.
//
// Idempotency:
//
//  1. For intents WITHOUT a `build:` block, Apply short-circuits as a
//     no-op when opts.Existing.IntentHash == opts.IntentHash — same
//     intent.yaml content, nothing to do regardless of node status.
//  2. For intents WITH a `build:` block, the source tree can change
//     even when intent.yaml itself doesn't. In that case Apply first
//     resolves the build (cache-hit-fast: ~150ms when nothing
//     changed) and short-circuits ONLY when BOTH the intent hash AND
//     the resolved build cache key match the existing managed node.
//     This closes the dev-loop bug where editing a `.java` file
//     without touching intent.yaml would silently no-op.
//
// Endpoints is reconstructed from the intent's port spec on the no-op
// path: apply.schema.json declares it as an object and emitting
// `null` (the zero value of map[string]string) violates the contract
// for agents that always expect host:port pairs to probe.
func Apply(ctx context.Context, opts Options) (*Result, error) {
	if err := validateOptions(opts); err != nil {
		return nil, err
	}

	// Safety gate (opt-in): refuse a non-private intent before any side
	// effect. Enforced here in the core so it covers every entry point,
	// not just `trond apply`. Returns a typed StructuredError so callers
	// surface error_code PRIVATE_NETWORK_REQUIRED / exit 2 unchanged.
	if opts.RequirePrivate && !intent.IsPrivate(opts.Intent.Network) {
		return nil, output.NewError("PRIVATE_NETWORK_REQUIRED", output.ExitValidationError,
			fmt.Sprintf("--require-private set but intent network is %q (not private); refusing to apply", opts.Intent.Network))
	}

	start := time.Now()
	node := &opts.Intent.Nodes[0]

	// Fast path: intents without build blocks idempotency-gate on
	// intent hash alone (legacy behavior preserved).
	if node.Build == nil &&
		opts.Existing != nil && opts.Existing.IntentHash == opts.IntentHash {
		return noChangeResult(opts, nil, start), nil
	}

	// Resolve build before render. The artifact path it produces
	// feeds into the systemd unit (jar runtime) or the compose
	// image: field (docker runtime). Cache hit is < 200ms so this
	// is cheap to run unconditionally when build is present.
	// Failures surface as structured errors and abort apply.
	buildSummary, builtJarPath, builtImageTag, buildErr := resolveBuild(ctx, opts, node)
	if buildErr != nil {
		return nil, buildErr
	}

	// Build-aware idempotency: same intent AND same build cache key →
	// nothing changed end-to-end, even if the file timestamps moved.
	if node.Build != nil &&
		opts.Existing != nil &&
		opts.Existing.IntentHash == opts.IntentHash &&
		buildSummary != nil &&
		opts.Existing.BuildCacheKey == buildSummary.CacheKey {
		return noChangeResult(opts, buildSummary, start), nil
	}

	rendered, err := render.RenderHOCONWithSecrets(opts.TemplateDir, opts.Intent, node)
	if err != nil {
		return nil, fmt.Errorf("render hocon: %w", err)
	}
	// Deploy path: java-tron needs the real witness key inlined, and
	// config_hash must stay computed over those same bytes so
	// idempotency is unaffected by redaction on the preview paths.
	hocon := rendered.Deployable()
	configHash := sha256hex([]byte(hocon))

	jdk := opts.JDKVersion
	if jdk == 0 {
		jdk = detectJDK(ctx, opts.Target)
	}
	memGB := render.ParseMemoryGB(node.Resources.Memory)
	if memGB == 0 {
		memGB = 16
	}
	jvmArgs := render.JVMArgsString(memGB, jdk, node.JVM)

	runtimeType := opts.Intent.Target.Runtime
	if runtimeType == "" {
		// cobra apply always pipes the intent through intent.Parse →
		// ApplyDefaults, so Runtime should already be filled. This
		// fallback covers programmatic callers (recipe, MCP, ad-hoc
		// tests) that bypass ApplyDefaults — defer to the shared
		// rule (intent.DefaultRuntime) so they get the same "docker
		// unless build present → jar" behavior, not a silently
		// drifted local default.
		runtimeType = intent.DefaultRuntime(opts.Intent)
	}

	deployOpts := runtime.DeployOpts{
		Name:       opts.Intent.Name,
		ConfigData: []byte(hocon),
		EnvVars:    opts.EnvVars,
	}

	switch runtimeType {
	case "docker":
		// builtImageTag is non-empty when intent had `build:` with
		// artifact: image — render against the local tag + emit
		// pull_policy: never so compose doesn't pull from a registry.
		deployOpts.ComposeData = []byte(render.RenderCompose(opts.Intent.Name, opts.Intent, node, "", jvmArgs, builtImageTag))
		rt := runtime.NewDockerRuntime(opts.Target, opts.DeploymentsDir)
		if err := rt.Deploy(ctx, deployOpts); err != nil {
			return nil, fmt.Errorf("docker deploy: %w", err)
		}
	case "jar":
		// Phase 4: when intent has `build:` + target is SSH, scp the
		// locally-built JAR to the remote BEFORE rendering the
		// systemd unit. The unit's ExecStart will reference the
		// remote-side path. Local targets keep the cache-path
		// reference (no transfer needed; systemd reads the cache
		// path directly on the same fs).
		jarPath := builtJarPath // empty when node has no `build:` block
		if builtJarPath != "" && opts.Intent.Target.Type == "ssh" {
			remoteJar := filepath.Join(node.InstallPath, "FullNode.jar")
			if err := transferBuiltJAR(ctx, opts.Target, buildSummary, builtJarPath, remoteJar); err != nil {
				return nil, err
			}
			jarPath = remoteJar
		}
		deployOpts.SystemdData = []byte(render.RenderSystemdUnit(opts.Intent, node, jvmArgs, jarPath, ""))
		// JarPath still points at the install_path location for `mkdir -p` /
		// config layout; only the systemd ExecStart references jarPath above.
		deployOpts.JarPath = filepath.Join(node.InstallPath, "FullNode.jar")
		if node.Jar != nil {
			deployOpts.JarURL = node.Jar.URL
			deployOpts.JarSHA256 = node.Jar.SHA256
		}
		rt := runtime.NewJarRuntime(opts.Target)
		if err := rt.Deploy(ctx, deployOpts); err != nil {
			return nil, fmt.Errorf("jar deploy: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtimeType)
	}

	// Deploy monitoring stack if enabled.
	monRes := deployMonitoring(ctx, opts, node, runtimeType)

	// Persist into state.
	managed := state.ManagedNode{
		Name:       opts.Intent.Name,
		IntentHash: opts.IntentHash,
		ConfigHash: configHash,
		Version:    node.Version,
		Network:    opts.Intent.Network,
		Target: state.NodeTarget{
			Type:         opts.Intent.Target.Type,
			Host:         opts.Intent.Target.Host,
			User:         opts.Intent.Target.User,
			Port:         opts.Intent.Target.Port,
			IdentityFile: opts.Intent.Target.IdentityFile,
		},
		Runtime:     runtimeType,
		Status:      "running",
		LastApplied: time.Now().UTC(),
		HTTPPort:    node.Ports.HTTP,
		GRPCPort:    node.Ports.GRPC,
		MetricsPort: node.Ports.Metrics,
		Labels:      node.Labels,
		InstallPath: node.InstallPath,
		Monitoring:  monRes.managed,
	}
	outcome := "created"
	if opts.Existing != nil {
		managed.PreviousVersion = opts.Existing.Version
		outcome = "updated"
	}
	if buildSummary != nil {
		managed.BuildCacheKey = buildSummary.CacheKey
	}
	opts.Store.UpsertNode(opts.State, managed)
	if err := opts.Store.Save(opts.State); err != nil {
		return nil, fmt.Errorf("save state: %w", err)
	}

	deployedMs := time.Since(start).Milliseconds()
	res := &Result{
		Name:       opts.Intent.Name,
		Outcome:    outcome,
		IntentHash: opts.IntentHash,
		ConfigHash: configHash,
		Version:    node.Version,
		Runtime:    runtimeType,
		Network:    opts.Intent.Network,
		IsPrivate:  intent.IsPrivate(opts.Intent.Network),
		Endpoints: map[string]string{
			"http": fmt.Sprintf("http://127.0.0.1:%d", node.Ports.HTTP),
			"grpc": fmt.Sprintf("127.0.0.1:%d", node.Ports.GRPC),
		},
		DurationMs:          deployedMs,
		Build:               buildSummary,
		MonitoringError:     monRes.error,
		MonitoringEndpoints: monRes.urls,
	}
	_ = builtImageTag // consumed by Phase 3 (docker runtime + image artifact)

	if opts.Wait {
		waitErr := WaitForReady(ctx, opts.Target, opts.Intent.Name, node.Ports.HTTP, opts.WaitTimeout)
		res.WaitedMs = time.Since(start).Milliseconds() - deployedMs
		ready := waitErr == nil
		res.Ready = &ready
		if waitErr != nil {
			res.WaitError = waitErr.Error()
		}
	}
	return res, nil
}

func validateOptions(o Options) error {
	switch {
	case o.Intent == nil:
		return fmt.Errorf("Intent is required")
	case len(o.Intent.Nodes) == 0:
		return fmt.Errorf("Intent.Nodes is empty")
	case o.Target == nil:
		return fmt.Errorf("Target is required")
	case o.Store == nil:
		return fmt.Errorf("Store is required")
	case o.State == nil:
		return fmt.Errorf("State is required")
	case o.IntentHash == "":
		return fmt.Errorf("IntentHash is required")
	}
	// Defense-in-depth: callers SHOULD pre-validate via intent.Validate()
	// (cobra apply does), but recipe / MCP / programmatic callers may
	// bypass that. Enforce the artifact-source mutex here so a malformed
	// caller can't deploy a node with both Build and Image both wired.
	// Spec/002 FR-005.
	n := &o.Intent.Nodes[0]
	sources := 0
	if n.Build != nil {
		sources++
	}
	if n.Image != "" {
		sources++
	}
	if n.Jar != nil {
		sources++
	}
	if sources > 1 {
		return output.NewErrorf("VALIDATION_ERROR", output.ExitValidationError,
			"node %q: build, image, jar are mutually exclusive (pick one artifact source)", n.Type)
	}

	// Phase 2 only wires artifact=jar end-to-end. The artifact_kind
	// must match the runtime, otherwise we'd render a docker compose
	// with `image: ""` (because the Image default is suppressed when
	// Build is present) or a systemd unit pointing at a non-existent
	// JAR. Reject the mismatch up-front. Phase 3 lifts this when
	// the docker runtime learns to consume artifact=image.
	if n.Build != nil {
		rt := o.Intent.Target.Runtime
		if rt == "" {
			rt = "jar" // build present → defaults to jar (matches applyTargetDefaults)
		}
		artifact := n.Build.Artifact
		if artifact == "" {
			artifact = "jar"
		}
		switch {
		case rt == "docker" && artifact == "jar":
			return output.NewErrorf("VALIDATION_ERROR", output.ExitValidationError,
				"node %q: target.runtime=docker cannot consume build.artifact=jar — set build.artifact=image (docker path) or switch target.runtime=jar", n.Type)
		case rt == "jar" && artifact == "image":
			return output.NewErrorf("VALIDATION_ERROR", output.ExitValidationError,
				"node %q: target.runtime=jar cannot consume build.artifact=image — set build.artifact=jar or switch runtime", n.Type)
		}
		// Mirror intent.Validate's cross-arch-image guard so callers
		// bypassing intent.Validate still get rejected. See loader.go
		// for the full rationale. jar-wrap strategy is safe across
		// arches (no docker.sock bind-mount); only gradle strategy
		// has the silent wrong-arch hazard.
		imgStrategy := n.Build.ImageStrategy
		if imgStrategy == "" {
			imgStrategy = "gradle"
		}
		if artifact == "image" && imgStrategy == "gradle" && n.Build.Platform != "" {
			hostPlatform := intent.DefaultPlatform()
			if n.Build.Platform != hostPlatform {
				return output.NewErrorf("VALIDATION_ERROR", output.ExitValidationError,
					"node %q: build.artifact=image + image_strategy=gradle with platform=%q is unsafe on host=%q — switch to image_strategy=jar-wrap for cross-arch",
					n.Type, n.Build.Platform, hostPlatform)
			}
		}
	}
	return nil
}

// sha256hex hashes a byte slice as lower-case hex. Mirrors the helper
// in cmd/apply.go; duplicated here so this package has no cmd import.
// noChangeResult constructs the no-op `Outcome: "no_change"`
// envelope shared between the no-build fast path and the
// build-aware short-circuit. Threading buildSummary keeps the result
// consistent regardless of which gate fired — agents always see
// the same shape.
//
// PRECONDITION: opts.Existing must be non-nil. Both callers (the
// pre-build no_change gate and the build-aware gate) check this
// before invoking — the helper itself trusts the contract to keep
// the body straight-line.
func noChangeResult(opts Options, buildSummary *BuildSummary, start time.Time) *Result {
	// Backfill the network on legacy state. A node deployed before
	// ManagedNode.Network existed has it empty; on a no-op re-apply we
	// learn it from the intent, so persist it — otherwise the no_change
	// Result would report network/is_private from the intent while a
	// follow-up `status` (which reads state) still showed it absent.
	// Best-effort: a save failure here doesn't fail the no-op.
	if opts.Existing != nil && opts.Existing.Network != opts.Intent.Network && opts.Store != nil && opts.State != nil {
		opts.Existing.Network = opts.Intent.Network
		opts.Store.UpsertNode(opts.State, *opts.Existing)
		_ = opts.Store.Save(opts.State)
	}

	ports := opts.Intent.Nodes[0].Ports
	return &Result{
		Name:       opts.Intent.Name,
		Outcome:    "no_change",
		IntentHash: opts.IntentHash,
		ConfigHash: opts.Existing.ConfigHash,
		Version:    opts.Existing.Version,
		Runtime:    opts.Existing.Runtime,
		Network:    opts.Intent.Network,
		IsPrivate:  intent.IsPrivate(opts.Intent.Network),
		Endpoints: map[string]string{
			"http": fmt.Sprintf("http://127.0.0.1:%d", ports.HTTP),
			"grpc": fmt.Sprintf("127.0.0.1:%d", ports.GRPC),
		},
		DurationMs:          time.Since(start).Milliseconds(),
		Build:               buildSummary,
		MonitoringEndpoints: monitoringEndpointsFromExisting(opts.Existing),
	}
}

// monitoringEndpointsFromExisting returns monitoring URLs from a managed
// node's saved state, if monitoring was deployed for it.
func monitoringEndpointsFromExisting(existing *state.ManagedNode) map[string]string {
	if existing == nil || existing.Monitoring == nil || !existing.Monitoring.Enabled {
		return nil
	}
	return map[string]string{
		"prometheus_url": fmt.Sprintf("http://127.0.0.1:%d", existing.Monitoring.PrometheusPort),
		"grafana_url":    fmt.Sprintf("http://127.0.0.1:%d", existing.Monitoring.GrafanaPort),
	}
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// IntentHashFromBytes is a convenience for callers that already hold
// the raw intent.yaml bytes (cmd/apply.go has them after os.ReadFile;
// MCP has them after reading the file in a tool handler).
func IntentHashFromBytes(data []byte) string {
	return sha256hex(data)
}

// detectJDK probes the target for an installed Java version. Returns
// 17 when detection fails — most TRON 4.x builds ship with JDK 17
// tuning defaults and the G1 args we select are safe across modern
// JDKs. Mirrors cmd/apply.go's helper. Uses stdlib strings/strconv
// directly; the earlier inline-helpers version had subtle behavioral
// drift from strconv.Atoi (e.g. silently returning 0 on whitespace).
func detectJDK(ctx context.Context, tgt target.Target) int {
	out, err := tgt.Exec(ctx, "java", "-version")
	if err != nil {
		return 17
	}
	s := string(out)
	idx := strings.IndexByte(s, '"')
	if idx < 0 {
		return 17
	}
	rest := s[idx+1:]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return 17
	}
	ver := strings.TrimPrefix(rest[:end], "1.")
	if dot := strings.IndexByte(ver, '.'); dot > 0 {
		ver = ver[:dot]
	}
	n, err := strconv.Atoi(strings.TrimSpace(ver))
	if err != nil || n <= 0 {
		return 17
	}
	return n
}

// monitoringResult carries the outcome of deployMonitoring back to the
// Apply caller so it can populate Result and ManagedNode fields.
type monitoringResult struct {
	error   string
	urls    map[string]string
	managed *state.MonitoringState
}

// deployMonitoring deploys the Prometheus + Grafana stack if monitoring is
// enabled in the intent. For docker runtime it co-locates with the node;
// for jar runtime it deploys to the trond machine (local target).
func deployMonitoring(ctx context.Context, opts Options, node *intent.NodeSpec, runtimeType string) monitoringResult {
	if opts.Intent.Monitoring == nil || opts.Intent.Monitoring.Enabled == nil || !*opts.Intent.Monitoring.Enabled {
		return monitoringResult{}
	}

	// Build scrape target address.
	var scrapeAddr string
	switch runtimeType {
	case "docker":
		// Within the docker compose network, use the container name.
		scrapeAddr = fmt.Sprintf("%s:%d", opts.Intent.Name, node.Ports.Metrics)
	case "jar":
		if opts.Intent.Target.Type == "ssh" {
			scrapeAddr = fmt.Sprintf("%s:%d", opts.Intent.Target.Host, node.Ports.Metrics)
		} else {
			scrapeAddr = fmt.Sprintf("127.0.0.1:%d", node.Ports.Metrics)
		}
	}

	targets := []render.MonitoringTarget{{
		Name:    opts.Intent.Name,
		Address: scrapeAddr,
		Labels: map[string]string{
			"group":    "group-tron",
			"instance": opts.Intent.Name,
			"network":  opts.Intent.Network,
		},
	}}

	// Determine where monitoring runs and which network to attach.
	var monTarget target.Target
	var monWorkDir string
	var networkName string
	if runtimeType == "jar" && opts.Intent.Target.Type == "ssh" {
		// Jar + SSH: monitoring runs on the trond machine.
		monTarget = target.NewLocalTarget()
		monWorkDir = paths.Deployments()
	} else {
		// Docker (local or SSH): monitoring co-locates with the node.
		monTarget = opts.Target
		monWorkDir = opts.DeploymentsDir
		if runtimeType == "docker" {
			networkName = opts.Intent.Name + "_default"
		}
	}

	// Render configs.
	composeData := render.RenderMonitoringCompose(opts.Intent.Name, opts.Intent, targets, networkName)
	promConfig := render.RenderPrometheusConfig(targets, opts.Intent.Monitoring.Prometheus.Retention)
	dsYAML, provYAML := render.RenderGrafanaProvisioning(
		fmt.Sprintf("http://prometheus:%d", opts.Intent.Monitoring.Prometheus.Port),
	)

	// Load embedded dashboards.
	dashboards := make(map[string][]byte)
	for _, name := range render.DashboardNames() {
		data, err := render.LoadDashboard(name)
		if err != nil {
			continue
		}
		dashboards[name] = render.NormalizeDashboard(data)
	}

	monOpts := runtime.MonitoringDeployOpts{
		Name:              opts.Intent.Name,
		ComposeData:       []byte(composeData),
		PrometheusConfig:  []byte(promConfig),
		GrafanaDatasource: []byte(dsYAML),
		GrafanaProvider:   []byte(provYAML),
		Dashboards:        dashboards,
	}

	rt := runtime.NewMonitoringRuntime(monTarget, monWorkDir)
	if err := rt.Deploy(ctx, monOpts); err != nil {
		return monitoringResult{error: err.Error()}
	}

	m := opts.Intent.Monitoring
	urls := map[string]string{
		"grafana_url": fmt.Sprintf("http://127.0.0.1:%d", m.Grafana.Port),
	}
	if m.Prometheus.Port > 0 {
		urls["prometheus_url"] = fmt.Sprintf("http://127.0.0.1:%d", m.Prometheus.Port)
	}
	return monitoringResult{
		urls: urls,
		managed: &state.MonitoringState{
			Enabled:        true,
			PrometheusPort: m.Prometheus.Port,
			GrafanaPort:    m.Grafana.Port,
			TargetType:     opts.Intent.Target.Type,
		},
	}
}

package intent

// Intent is the top-level declarative file describing desired node state.
type Intent struct {
	Name       string      `yaml:"name" json:"name" validate:"required,hostname_rfc1123"`
	Target     Target      `yaml:"target" json:"target" validate:"required"`
	Network    string      `yaml:"network" json:"network" validate:"required,oneof=mainnet nile private"`
	Nodes      []NodeSpec  `yaml:"nodes" json:"nodes" validate:"required,min=1,dive"`
	Monitoring *Monitoring `yaml:"monitoring,omitempty" json:"monitoring,omitempty"`
}

// Target specifies where to deploy.
type Target struct {
	Type         string `yaml:"type" json:"type" validate:"required,oneof=local ssh"`
	Host         string `yaml:"host,omitempty" json:"host,omitempty" validate:"required_if=Type ssh,omitempty,safe_string"`
	User         string `yaml:"user,omitempty" json:"user,omitempty" validate:"required_if=Type ssh,omitempty,safe_string"`
	Port         int    `yaml:"port,omitempty" json:"port,omitempty"`
	IdentityFile string `yaml:"identity_file,omitempty" json:"identity_file,omitempty" validate:"omitempty,safe_string"`
	Runtime      string `yaml:"runtime,omitempty" json:"runtime,omitempty" validate:"omitempty,oneof=docker jar"`
	// AutoPorts replaces every node port that resolves to a default value
	// (8090, 50051, 18888, …) with a free OS-assigned port. This lets
	// concurrent test enclaves spin up in parallel without manually staging
	// non-overlapping port plans in each intent file.
	AutoPorts bool `yaml:"auto_ports,omitempty" json:"auto_ports,omitempty"`
}

// NodeSpec defines a single node's desired configuration.
type NodeSpec struct {
	Type           string      `yaml:"type" json:"type" validate:"required,oneof=fullnode witness solidity lite"`
	Version        string      `yaml:"version,omitempty" json:"version,omitempty" validate:"omitempty,safe_string"`
	Image          string      `yaml:"image,omitempty" json:"image,omitempty" validate:"omitempty,safe_string"`
	InstallPath    string      `yaml:"install_path,omitempty" json:"install_path,omitempty" validate:"omitempty,safe_string"`
	ProcessManager string      `yaml:"process_manager,omitempty" json:"process_manager,omitempty" validate:"omitempty,oneof=systemd nohup"`
	SystemUser     string      `yaml:"system_user,omitempty" json:"system_user,omitempty" validate:"omitempty,safe_string"`
	WitnessKeyEnv  string      `yaml:"witness_key_env,omitempty" json:"witness_key_env,omitempty"`
	Features       Features    `yaml:"features,omitempty" json:"features,omitempty"`
	Resources      Resources   `yaml:"resources,omitempty" json:"resources,omitempty"`
	JVM            *JVMConfig  `yaml:"jvm,omitempty" json:"jvm,omitempty"`
	Ports          PortMapping `yaml:"ports,omitempty" json:"ports,omitempty"`
	Storage        Storage     `yaml:"storage,omitempty" json:"storage,omitempty"`

	// Restart maps to docker-compose's "restart:" or systemd's "Restart=".
	// Allowed values mirror docker-compose: no, on-failure, always,
	// unless-stopped (default). The systemd renderer translates these to
	// the closest equivalent (Restart=no | on-failure | always | always).
	Restart string `yaml:"restart,omitempty" json:"restart,omitempty" validate:"omitempty,oneof=no on-failure always unless-stopped"`

	// ExtraEnv injects arbitrary environment variables into the runtime.
	// For Docker this becomes a flat list under "environment:". For jar
	// runtime it becomes a systemd drop-in [Service] Environment= line.
	// Witness key passthrough is still automatic (don't list it here).
	ExtraEnv map[string]string `yaml:"extra_env,omitempty" json:"extra_env,omitempty"`

	// ExtraArgs appends positional arguments to the FullNode command line
	// after "-c <conf>" but before "--witness". Useful for image-specific
	// flags like --log-config or experimental switches.
	ExtraArgs []string `yaml:"extra_args,omitempty" json:"extra_args,omitempty"`

	// Labels become docker labels (or are stored verbatim in state for jar
	// nodes). Test harnesses use them to filter `docker ps -f label=...`.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`

	// NetworkOverrides controls java-tron's networking section in the
	// rendered HOCON: seed nodes, explicit peers, p2p protocol version,
	// discovery toggle, connection caps, sync-check switch. These are the
	// fields any private network or test enclave realistically needs to
	// touch — they're fielded out instead of left to config_overrides so
	// validation can catch typos.
	NetworkOverrides NetworkOverrides `yaml:"network_overrides,omitempty" json:"network_overrides,omitempty"`

	// WitnessKey configures how a witness node's signing key is delivered.
	// Mutually exclusive with the legacy WitnessKeyEnv at the top level —
	// when WitnessKey is non-zero it takes precedence and the env-only
	// shortcut is ignored. Only meaningful for type: witness.
	WitnessKey *WitnessKey `yaml:"witness_key,omitempty" json:"witness_key,omitempty"`

	// ConfigOverrides is the long-tail HOCON escape hatch: keys that
	// rarely need a typed field but still need to be overridable. The map
	// values land verbatim in a "trond overrides" block appended to the
	// rendered HOCON, where HOCON's last-write-wins semantics replace
	// whatever the template said.
	//
	// Example:
	//   config_overrides:
	//     "vm.supportConstant": true
	//     "block.maintenanceTimeInterval": 30000
	//     "storage.db.engine": "ROCKSDB"
	ConfigOverrides map[string]any `yaml:"config_overrides,omitempty" json:"config_overrides,omitempty"`

	// --- Compose-only fields (no-op for jar runtime) ---

	// Networks attaches the container to one or more pre-existing docker
	// networks (declared external) instead of the auto-generated default.
	// Useful when the harness wants chaos primitives or shared
	// observability traffic on a known network.
	Networks []string `yaml:"networks,omitempty" json:"networks,omitempty"`

	// DependsOn produces a "depends_on:" list in compose so a node only
	// starts after the named services are up. Names must reference other
	// nodes in the same intent / network.
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`

	// Healthcheck wires docker's native HEALTHCHECK directive. Independent
	// from trond's own wait/health/diagnose — useful when downstream tools
	// (orchestrators, CI dashboards) consume docker's health state.
	Healthcheck *Healthcheck `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`

	// Ulimits exposes the file-descriptor cap, which java-tron is
	// sensitive to under heavy peer loads.
	Ulimits *Ulimits `yaml:"ulimits,omitempty" json:"ulimits,omitempty"`

	// ExtraHosts produces "extra_hosts:" entries for /etc/hosts injection
	// inside the container — handy when peers are reached by hostname and
	// real DNS isn't configured (multi-node tests on a single host).
	ExtraHosts map[string]string `yaml:"extra_hosts,omitempty" json:"extra_hosts,omitempty"`

	// Entrypoint overrides the image entrypoint. Use with care — the
	// official java-tron entrypoint expects to receive the same args we
	// emit; replacing it means you take responsibility for the lifecycle.
	Entrypoint []string `yaml:"entrypoint,omitempty" json:"entrypoint,omitempty"`

	// Logging configures docker's log driver. Empty values mean "let
	// docker decide" (which is usually json-file with no rotation).
	Logging *Logging `yaml:"logging,omitempty" json:"logging,omitempty"`

	// ShmSize sets /dev/shm. Only relevant for some VM/EVM workloads;
	// java-tron's default is fine for most cases.
	ShmSize string `yaml:"shm_size,omitempty" json:"shm_size,omitempty"`

	// Jar configures where to fetch the FullNode.jar for jar-runtime
	// targets. Without it, trond assumes the operator has pre-placed the
	// jar at <install_path>/FullNode.jar — which is fine for AMI / Ansible
	// flows but breaks first-time provisioning. Only consulted when the
	// target's runtime is "jar".
	Jar *JarSource `yaml:"jar,omitempty" json:"jar,omitempty"`

	// Build, when present, makes trond produce the deploy artifact
	// itself from a local java-tron source tree, instead of pulling a
	// pre-built Image / Jar. Mutually exclusive with `image:` and
	// `jar:` — the deploy artifact has exactly one source. The full
	// design is in specs/002-trond-build-pipeline/. Phase 2 wires this
	// into apply; Phase 3 adds artifact=image; Phase 4 adds SSH
	// targets.
	Build *BuildSpec `yaml:"build,omitempty" json:"build,omitempty"`
}

// BuildSpec describes how to produce a deploy artifact from a local
// java-tron source tree. See specs/002-trond-build-pipeline/spec.md
// for the field-level semantics. The validator + apply pipeline
// reject any combination where `build:` coexists with `image:` or
// `jar:` on the same node.
//
// Defaults applied at load time:
//   - JDK: "8"
//   - Artifact: "jar"
//   - Builder: "docker"
//   - Revision: "HEAD"
//   - GradleTask: derived from Artifact ("shadowJar" for jar,
//     "dockerBuild" for image; spec FR-001 lets users override).
type BuildSpec struct {
	// Source is the path to the java-tron source tree. Per FR-021,
	// relative paths resolve against the intent file's directory
	// (matches docker-compose's build.context convention). The CLI
	// `--source` flag resolves against CWD instead; loader.go applies
	// the intent-side rule before validation runs.
	Source string `yaml:"source" json:"source" validate:"required,safe_string"`

	// Revision selects which git revision to build. "HEAD" (default)
	// honors dirty working-tree state and folds it into the cache key
	// (FR-002). Branch/tag/sha values resolve to that exact commit
	// and ignore dirty state.
	Revision string `yaml:"revision,omitempty" json:"revision,omitempty" validate:"omitempty,safe_string"`

	// JDK selects the builder container's JDK version. The pinned
	// digest is resolved at apply time from internal/build/pins/.
	JDK string `yaml:"jdk,omitempty" json:"jdk,omitempty" validate:"omitempty,oneof=8 11 17 21"`

	// Artifact selects what to produce. Phase 2 supports only "jar";
	// "image" is Phase 3.
	Artifact string `yaml:"artifact,omitempty" json:"artifact,omitempty" validate:"omitempty,oneof=jar image"`

	// ImageTag is the local Docker tag applied when Artifact=image
	// (validated against Docker reference format at apply time per
	// FR-005). Required when Artifact=image.
	ImageTag string `yaml:"image_tag,omitempty" json:"image_tag,omitempty" validate:"omitempty,safe_string"`

	// Builder picks between the containerized builder (default) and
	// the host's local gradle. Phase 5 implements --builder host.
	Builder string `yaml:"builder,omitempty" json:"builder,omitempty" validate:"omitempty,oneof=docker host"`

	// GradleTask overrides the default gradle task name. Sensible
	// defaults are derived from Artifact (jar → shadowJar, image →
	// dockerBuild). Token-regex-validated at apply time per FR-022.
	GradleTask string `yaml:"gradle_task,omitempty" json:"gradle_task,omitempty" validate:"omitempty,safe_string"`

	// GradleArgs are extra arguments forwarded to gradle. Restricted
	// by the flag-name allowlist in internal/build (FR-022): things
	// like `--offline`, `-D<k>=<v>`, `-P<k>=<v>` pass; `--init-script`
	// and similar do not.
	GradleArgs []string `yaml:"gradle_args,omitempty" json:"gradle_args,omitempty"`

	// BuilderImageOverride bypasses the embedded pin for `JDK`. Escape
	// hatch — use when the pinned digest becomes unreachable. The
	// override value participates in the cache key (FR-024) so pinned
	// and overridden builds don't collide.
	BuilderImageOverride string `yaml:"builder_image_override,omitempty" json:"builder_image_override,omitempty" validate:"omitempty,safe_string"`

	// Env is a passthrough map for the build container. The keys are
	// restricted to the FR-019 allowlist (GRADLE_OPTS, JAVA_OPTS,
	// GRADLE_USER_HOME, MAVEN_OPTS, ORG_GRADLE_PROJECT_*). Values are
	// shell-safe under argv (FR-022).
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// Platform selects the builder container's CPU architecture
	// (`linux/amd64`, `linux/arm64`). Empty defaults to the host's
	// arch via intent.DefaultPlatform. Set explicitly to cross-build
	// against the java-tron platform matrix:
	//
	//   linux/amd64 → JDK 8  (only combo java-tron supports on Intel)
	//   linux/arm64 → JDK 17 (only combo java-tron supports on ARM)
	//
	// Building a different arch from the host (e.g. amd64 from an M1
	// Mac) works via docker's QEMU emulation — 3-5× slower but
	// functional. Two builds with different platforms coexist in the
	// cache (Platform participates in the cache key).
	Platform string `yaml:"platform,omitempty" json:"platform,omitempty" validate:"omitempty,oneof=linux/amd64 linux/arm64"`

	// ImageStrategy picks how `artifact: image` produces the image.
	// Only meaningful when artifact = image; ignored for jar.
	//
	//   "gradle" (default): invoke the source tree's gradle docker
	//     plugin task (e.g. `dockerBuild`). Requires the source to
	//     have such a task — works for tron-docker-style repos that
	//     ship gradle docker config, NOT for stock java-tron which
	//     only emits JARs.
	//
	//   "jar-wrap": Phase 5d. First produce the JAR (recurses through
	//     the artifact=jar path with full cache reuse), then run
	//     `docker build` against a trond-embedded minimal Dockerfile
	//     that COPYs the JAR into a pinned eclipse-temurin runtime
	//     base. Works for stock java-tron — the source tree only
	//     needs a JAR task (e.g. `:framework:buildFullNodeJar`).
	ImageStrategy string `yaml:"image_strategy,omitempty" json:"image_strategy,omitempty" validate:"omitempty,oneof=gradle jar-wrap"`

	// Patches is an optional ordered list of unified-diff files to
	// apply against the resolved source revision before gradle runs
	// (FR-026). Use cases: testing an unmerged upstream PR; backporting
	// fixes; instrumentation for stress / replay testing; security or
	// compliance variants not yet upstreamed.
	//
	// Path resolution: each entry is intent-relative (FR-021 mirror).
	// `../tron-deployment/tools/replay/patches/01-skip-expiration.patch`
	// from an intent at `~/projects/my-net/intent.yaml` resolves to
	// `~/projects/my-net/../tron-deployment/tools/replay/patches/...`.
	//
	// Behavior when non-empty:
	//   - Build runs in an isolated `git worktree` at the resolved
	//     revision (your primary source tree is NEVER mutated).
	//   - patch_hash = sha256(canonical concat of patch file contents
	//     in declared order) — a pure function of inputs, NOT git diff
	//     of the working tree. Same intent + same patches → same cache
	//     key on every machine, no matter what local WIP edits the
	//     operator has.
	//
	// When empty/absent: build pipeline behavior is unchanged from
	// Phase 1-5. The build runs in the source tree directly; patch_hash
	// follows the existing dirty-tree semantics (machine-local).
	Patches []string `yaml:"patches,omitempty" json:"patches,omitempty" validate:"omitempty,dive,safe_string"`
}

// JarSource tells trond where (and how) to fetch the java-tron jar for a
// jar-runtime deployment.
//
// SECURITY: when URL is set, SHA256 is mandatory and must be a 64-char
// lowercase hex digest. We also reject anything but `https://` schemes
// — without TLS + integrity, an attacker on any network hop (or a typo
// pointing at a hostile mirror) can drop a malicious jar into the
// systemd ExecStart and gain `system_user`-as-root code execution.
type JarSource struct {
	URL    string `yaml:"url" json:"url" validate:"omitempty,https_url"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty" validate:"required_with=URL,omitempty,sha256_hex"`
}

// NetworkOverrides surfaces the typical java-tron networking knobs as
// first-class intent fields. Anything left zero is omitted from the
// override block (the template's value wins).
type NetworkOverrides struct {
	// Seeds replaces seed.node.ip.list. Empty list means "leave template
	// default"; an explicit empty array `[]` (not nil) means "no seeds"
	// and renders as an empty list — useful for fully isolated tests.
	Seeds *[]string `yaml:"seeds,omitempty" json:"seeds,omitempty"`

	// ActivePeers and PassivePeers map to node.active / node.passive.
	// Same nil-vs-empty semantics as Seeds.
	ActivePeers  *[]string `yaml:"active_peers,omitempty" json:"active_peers,omitempty"`
	PassivePeers *[]string `yaml:"passive_peers,omitempty" json:"passive_peers,omitempty"`

	// P2PVersion replaces node.p2p.version. Setting this to a unique
	// value isolates the enclave from public networks even if the seed
	// list slips through.
	P2PVersion *int `yaml:"p2p_version,omitempty" json:"p2p_version,omitempty"`

	// Discovery toggles node.discovery.enable. False stops the node
	// broadcasting its presence — recommended for closed test enclaves.
	Discovery *bool `yaml:"discovery,omitempty" json:"discovery,omitempty"`

	// MaxConnections / MaxActiveSameIP map to node.maxConnections /
	// node.maxActiveNodesWithSameIp. Useful when collocating many nodes
	// on one host.
	MaxConnections  *int `yaml:"max_connections,omitempty" json:"max_connections,omitempty"`
	MaxActiveSameIP *int `yaml:"max_active_same_ip,omitempty" json:"max_active_same_ip,omitempty"`

	// NeedSyncCheck maps to block.needSyncCheck. Must be false on the
	// first node of a brand-new private network or it will hang waiting
	// for peers to sync from.
	NeedSyncCheck *bool `yaml:"need_sync_check,omitempty" json:"need_sync_check,omitempty"`
}

// WitnessKey describes how a witness node receives its signing key.
// Exactly one of {PrivateKeyEnv, KeystorePath} should be set.
type WitnessKey struct {
	// PrivateKeyEnv is the NAME of an env var holding the raw hex
	// private key. trond resolves the value at apply time and the key is
	// written into a `localwitness = ["${value}"]` line in the HOCON.
	PrivateKeyEnv string `yaml:"private_key_env,omitempty" json:"private_key_env,omitempty"`

	// KeystorePath is an absolute path inside the runtime to a JKS-style
	// keystore file. trond writes a `localwitnesskeystore = ["..."]`
	// HOCON line referencing it. The keystore itself must be present
	// (use `trond files put` or a baked image to deliver it).
	KeystorePath string `yaml:"keystore_path,omitempty" json:"keystore_path,omitempty"`

	// KeystorePasswordEnv (optional) names the env var that holds the
	// keystore password. The value is passed through as KEYSTORE_PASSWORD
	// to the runtime.
	KeystorePasswordEnv string `yaml:"keystore_password_env,omitempty" json:"keystore_password_env,omitempty"`

	// AccountAddress sets localWitnessAccountAddress for nodes operated
	// via witnessPermission delegation.
	AccountAddress string `yaml:"account_address,omitempty" json:"account_address,omitempty"`
}

// Healthcheck mirrors docker-compose's healthcheck block.
type Healthcheck struct {
	Test     []string `yaml:"test" json:"test" validate:"required,min=1"`
	Interval string   `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries  int      `yaml:"retries,omitempty" json:"retries,omitempty"`
	// StartPeriod gives the container a grace window before failures count.
	StartPeriod string `yaml:"start_period,omitempty" json:"start_period,omitempty"`
}

// Ulimits exposes the per-container ulimit knobs we actually care about
// for java-tron.
type Ulimits struct {
	NOFile int `yaml:"nofile,omitempty" json:"nofile,omitempty"`
}

// Logging maps to docker-compose logging.
type Logging struct {
	Driver  string            `yaml:"driver,omitempty" json:"driver,omitempty"`
	Options map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

// Features contains feature flags for a node.
//
// Each *bool is tri-state: nil means "use the template's default", an
// explicit true/false overrides. Render functions only emit overrides for
// fields they know how to translate into HOCON; unknown fields are no-ops.
type Features struct {
	Metrics        *bool `yaml:"metrics,omitempty" json:"metrics,omitempty"`
	JSONRPC        *bool `yaml:"jsonrpc,omitempty" json:"jsonrpc,omitempty"`
	RateLimit      *bool `yaml:"rate_limit,omitempty" json:"rate_limit,omitempty"`
	EventSubscribe *bool `yaml:"event_subscribe,omitempty" json:"event_subscribe,omitempty"`
}

// Resources specifies resource constraints.
type Resources struct {
	Memory string `yaml:"memory,omitempty" json:"memory,omitempty"`
	// CPU caps the container CPU. Docker compose accepts a fractional
	// string ("1.5") or an integer; we pass it through verbatim so users
	// keep full compose semantics. Empty means no limit.
	CPU string `yaml:"cpu,omitempty" json:"cpu,omitempty"`
}

// Storage controls how the chain DB and logs are persisted on the host.
// Both fields accept either:
//   - an absolute path (starts with "/"): bind-mounted as a host directory,
//     useful for keeping data outside Docker (snapshots, backups, dev).
//   - a bare name (no slash): treated as a docker named volume name,
//     letting two enclaves share data or letting tests pre-seed a volume.
//
// Empty values fall back to the per-node defaults "<name>-data" and
// "<name>-logs". If StoragePath is set, the data field is implicitly bound
// at "<StoragePath>/data" and "<StoragePath>/logs" — convenient when you
// just want one host root for everything.
type Storage struct {
	Data        string `yaml:"data,omitempty" json:"data,omitempty"`
	Logs        string `yaml:"logs,omitempty" json:"logs,omitempty"`
	StoragePath string `yaml:"path,omitempty" json:"path,omitempty"`
}

// JVMConfig provides optional JVM tuning overrides.
type JVMConfig struct {
	HeapMax      string `yaml:"heap_max,omitempty" json:"heap_max,omitempty"`
	HeapNew      string `yaml:"heap_new,omitempty" json:"heap_new,omitempty"`
	DirectMemory string `yaml:"direct_memory,omitempty" json:"direct_memory,omitempty"`
	GC           string `yaml:"gc,omitempty" json:"gc,omitempty" validate:"omitempty,oneof=G1 CMS auto"`
	GCLog        *bool  `yaml:"gc_log,omitempty" json:"gc_log,omitempty"`

	// ExtraOpts are additional JVM flags appended after everything trond
	// derives, so they win on any last-flag-wins option.
	//
	// The closed field set above covers heap and GC, which is most tuning
	// but not all of it. java-tron ships operational flags in
	// gradle/java-tron.vmoptions, read by its distribution launcher
	// (bin/FullNode); trond runs the JAR directly, so that file never
	// applies. -Dio.netty.allocator.type=pooled is the live example —
	// java-tron sets it to opt out of netty 4.2's adaptive allocator, and
	// with no escape hatch a trond-deployed node silently runs with the
	// allocator upstream deliberately avoided.
	//
	// Restricted to -D<key>=<value> and -XX:… — tuning, not code loading,
	// so -javaagent / -agentlib / -cp / @argfile stay out by construction
	// rather than by blocklist. Whitespace is rejected because JVMArgs
	// joins the set with spaces: an entry containing one would silently
	// become two arguments. Enforced in loader.go.
	ExtraOpts []string `yaml:"extra_opts,omitempty" json:"extra_opts,omitempty"`
}

// PortMapping defines custom port overrides.
type PortMapping struct {
	HTTP         int `yaml:"http,omitempty" json:"http,omitempty"`
	GRPC         int `yaml:"grpc,omitempty" json:"grpc,omitempty"`
	SolidityHTTP int `yaml:"solidity_http,omitempty" json:"solidity_http,omitempty"`
	SolidityGRPC int `yaml:"solidity_grpc,omitempty" json:"solidity_grpc,omitempty"`
	JSONRPC      int `yaml:"jsonrpc,omitempty" json:"jsonrpc,omitempty"`
	P2P          int `yaml:"p2p,omitempty" json:"p2p,omitempty"`
	Metrics      int `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

// Monitoring configures the optional Prometheus + Grafana monitoring
// stack that trond can deploy and manage alongside java-tron nodes.
type Monitoring struct {
	Enabled    *bool      `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Prometheus PromConfig `yaml:"prometheus,omitempty" json:"prometheus,omitempty"`
	Grafana    GrafConfig `yaml:"grafana,omitempty" json:"grafana,omitempty"`
}

// PromConfig configures the Prometheus scraper.
type PromConfig struct {
	Port      int    `yaml:"port,omitempty" json:"port,omitempty"`
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`
}

// GrafConfig configures the Grafana visualizer.
type GrafConfig struct {
	Port int `yaml:"port,omitempty" json:"port,omitempty"`

	// Expose publishes Grafana's host port on every interface (0.0.0.0)
	// instead of loopback only. Off by default: the grafana-oss image
	// ships a well-known default admin login, so a host-wide bind hands
	// the dashboards — and Grafana's datasource proxy, which can be
	// pointed at anything the host can reach — to whoever can reach the
	// port. Requires AdminPasswordEnv (see validateMonitoring).
	Expose bool `yaml:"expose,omitempty" json:"expose,omitempty"`

	// AdminPasswordEnv is the NAME of an environment variable holding the
	// Grafana admin password — never the password itself. The rendered
	// compose references it as ${NAME:?...}, so the variable must be set
	// in the environment that runs `docker compose up` (trond's own
	// environment for local targets); compose refuses to start the stack
	// when it is unset or empty.
	//
	// Grafana applies GF_SECURITY_ADMIN_PASSWORD when it initialises its
	// database, i.e. on the first start of a fresh grafana_data volume.
	// Setting it for an already-initialised stack does not rotate an
	// existing admin password.
	AdminPasswordEnv string `yaml:"admin_password_env,omitempty" json:"admin_password_env,omitempty"`
}

// BoolPtr is a helper for creating *bool values in intent construction.
func BoolPtr(v bool) *bool {
	return &v
}

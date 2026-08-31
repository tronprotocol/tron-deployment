package state

import (
	"strings"
	"time"
)

// ManagedNode represents a deployed node tracked in the state file.
type ManagedNode struct {
	Name           string `json:"name"`
	IntentHash     string `json:"intent_hash"`
	ConfigHash     string `json:"config_hash"`
	Version        string `json:"version"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
	// Network is the intent's `network:` value (mainnet | nile | private)
	// recorded at apply time, so `status` can report it and derive the
	// `is_private` safety fact without re-reading the intent. omitempty:
	// nodes deployed before this field existed report it absent.
	Network         string     `json:"network,omitempty"`
	Target          NodeTarget `json:"target"`
	Runtime         string     `json:"runtime"`
	Status          string     `json:"status"` // running, stopped, error, unknown
	LastApplied     time.Time  `json:"last_applied"`
	PreviousVersion string     `json:"previous_version,omitempty"`
	ComposePath     string     `json:"compose_path,omitempty"`
	SystemdUnit     string     `json:"systemd_unit,omitempty"`
	InstallPath     string     `json:"install_path,omitempty"`
	StorageRoot     string     `json:"storage_root,omitempty"`
	// HTTPPort and GRPCPort capture the API ports as configured at deploy time
	// so probe commands (health, diagnose, verify) can target the right port
	// without re-reading the intent file. Older state files predate these
	// fields — callers must fall back to defaults when zero.
	HTTPPort         int `json:"http_port,omitempty"`
	GRPCPort         int `json:"grpc_port,omitempty"`
	SolidityHTTPPort int `json:"solidity_http_port,omitempty"`
	SolidityGRPCPort int `json:"solidity_grpc_port,omitempty"`
	JSONRPCPort      int `json:"jsonrpc_port,omitempty"`
	// P2PPort is the listen.port a sibling can dial to peer with this node.
	// `network add` reads it from every existing entry to populate the new
	// node's active_peers so it can immediately join the P2P mesh.
	P2PPort int `json:"p2p_port,omitempty"`
	// MetricsPort is the Prometheus scrape target port (node.metrics.prometheus.port,
	// default 9527). Stored so network add's monitoring reload can build correct
	// scrape targets even when the user overrides ports.metrics in the intent.
	MetricsPort int `json:"metrics_port,omitempty"`
	// Labels mirror intent.NodeSpec.Labels and survive across CLI sessions
	// so test harnesses can filter via `trond list --label key=value`
	// without touching the original intent file.
	Labels map[string]string `json:"labels,omitempty"`

	// BuildCacheKey records the content-addressed build that produced
	// the artifact currently deployed for this node. Empty when the
	// node consumed a pre-built image or jar source. `trond build
	// prune` (FR-018) cross-references this field and refuses to
	// delete an artifact a running node depends on. Per spec/002 the
	// stored value is the full cache key (`<sha>-b<digest>[+dirty-...]`),
	// not just a git revision.
	BuildCacheKey string `json:"build_cache_key,omitempty"`

	// Monitoring tracks the deployed monitoring stack for this node.
	Monitoring *MonitoringState `json:"monitoring,omitempty"`
}

// BuildRevision returns the resolved source git revision (short, 12-hex)
// recorded in the build cache key, or "" when the node wasn't built from
// source (consumed a pre-built image/jar). The cache key is constructed as
// "<gitRevision[:12]>-b<digest>[+dirty-...][-x...]" (internal/build/key.go),
// so the revision is the leading hex segment before the first '-'. Lets an
// agent learn which java-tron commit a node is running without parsing the
// compound cache key itself.
func (n ManagedNode) BuildRevision() string {
	if n.BuildCacheKey == "" {
		return ""
	}
	rev := n.BuildCacheKey
	if i := strings.IndexByte(rev, '-'); i >= 0 {
		rev = rev[:i]
	}
	if len(rev) != 12 || strings.TrimLeft(rev, "0123456789abcdef") != "" {
		return ""
	}
	return rev
}

// MonitoringState records the Prometheus + Grafana stack deployed
// alongside a node.
type MonitoringState struct {
	Enabled        bool   `json:"enabled"`
	PrometheusPort int    `json:"prometheus_port"`
	GrafanaPort    int    `json:"grafana_port"`
	TargetType     string `json:"target_type,omitempty"` // "local" or "ssh"
}

// NodeTarget is the target info stored in state (subset of intent.Target).
type NodeTarget struct {
	Type         string `json:"type"`
	Host         string `json:"host,omitempty"`
	User         string `json:"user,omitempty"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

// DeploymentState is the top-level state file structure.
type DeploymentState struct {
	Version int           `json:"version"`
	Nodes   []ManagedNode `json:"nodes"`
}

// AuditEntry represents a single line in the audit log (JSONL format).
type AuditEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Command    string    `json:"command"`
	Node       string    `json:"node,omitempty"`
	Target     string    `json:"target"`
	IntentHash string    `json:"intent_hash,omitempty"`
	Result     string    `json:"result"` // success, failure, no_change
	DurationMs int64     `json:"duration_ms"`
	ErrorCode  string    `json:"error_code,omitempty"`
}

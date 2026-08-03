package intent

import (
	"fmt"
	"strings"
	"testing"
)

// fields_test.go is the field-by-field matrix for intent validation. The
// goal is to enumerate every typed field and exercise:
//   - representative valid values
//   - representative invalid values (where validation rejects them)
//   - default values applied when the field is omitted
//
// Tests here intentionally overlap a little with loader_test.go — they're
// organised by FIELD rather than by SCENARIO so a future change to a single
// field has one obvious test file to update.

// makeIntent builds a minimal-valid intent with overridable target/nodes.
// Returns the YAML bytes ready for Parse.
func makeIntent(extra string) []byte {
	base := `name: m
network: mainnet
target:
  type: local
nodes:
  - type: fullnode
`
	return []byte(base + extra)
}

// --- target.type / target.runtime ---

func TestTargetType_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"local", false},
		{"ssh", true}, // ssh requires host+user — without them, validation fails
		{"docker", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run("type="+tc.val, func(t *testing.T) {
			yaml := []byte("name: m\nnetwork: mainnet\ntarget:\n  type: " + tc.val + "\nnodes: [{type: fullnode}]\n")
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestTargetRuntime_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"docker", false},
		{"jar", false},
		{"", false}, // omitted → default "docker"
		{"podman", true},
		{"systemd", true},
	}
	for _, tc := range cases {
		t.Run("runtime="+tc.val, func(t *testing.T) {
			y := "target:\n  type: local\n  runtime: " + tc.val + "\n"
			if tc.val == "" {
				y = ""
			}
			yaml := []byte("name: m\nnetwork: mainnet\n" + y + "nodes: [{type: fullnode}]\n")
			if y == "" {
				yaml = []byte("name: m\nnetwork: mainnet\ntarget: {type: local}\nnodes: [{type: fullnode}]\n")
			}
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for runtime=%q", tc.val)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for runtime=%q: %v", tc.val, err)
			}
		})
	}
}

func TestTargetRuntime_DefaultsToDocker(t *testing.T) {
	i, err := Parse(makeIntent(""))
	if err != nil {
		t.Fatal(err)
	}
	if i.Target.Runtime != "docker" {
		t.Errorf("runtime default = %q, want docker", i.Target.Runtime)
	}
}

// --- target.auto_ports ---

func TestTargetAutoPorts(t *testing.T) {
	yaml := []byte(`
name: ap-test
network: mainnet
target:
  type: local
  auto_ports: true
nodes:
  - type: fullnode
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if !i.Target.AutoPorts {
		t.Error("AutoPorts not honored")
	}
	// With AutoPorts, the HTTP/GRPC defaults (8090/50051) should have been
	// replaced by allocated ports.
	if i.Nodes[0].Ports.HTTP == 8090 || i.Nodes[0].Ports.HTTP == 0 {
		t.Errorf("auto_ports did not replace HTTP default, got %d", i.Nodes[0].Ports.HTTP)
	}
	if i.Nodes[0].Ports.GRPC == 50051 || i.Nodes[0].Ports.GRPC == 0 {
		t.Errorf("auto_ports did not replace GRPC default, got %d", i.Nodes[0].Ports.GRPC)
	}
}

func TestTargetAutoPorts_NodesDontOverlap(t *testing.T) {
	yaml := []byte(`
name: ap-mn
network: private
target:
  type: local
  auto_ports: true
nodes:
  - type: fullnode
  - type: fullnode
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	a, b := i.Nodes[0].Ports, i.Nodes[1].Ports
	if a.HTTP == b.HTTP || a.GRPC == b.GRPC || a.P2P == b.P2P {
		t.Errorf("two nodes got overlapping auto-ports: %+v vs %+v", a, b)
	}
}

// --- target.port (SSH) ---

func TestTargetSSHPort_DefaultsTo22(t *testing.T) {
	yaml := []byte(`
name: sshd
network: mainnet
target:
  type: ssh
  host: example.com
  user: root
nodes: [{type: fullnode}]
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if i.Target.Port != 22 {
		t.Errorf("ssh port default = %d, want 22", i.Target.Port)
	}
}

func TestTargetSSHIdentityFile_DefaultExpanded(t *testing.T) {
	yaml := []byte(`
name: sshd
network: mainnet
target:
  type: ssh
  host: example.com
  user: root
nodes: [{type: fullnode}]
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if i.Target.IdentityFile == "" {
		t.Error("identity_file default not applied")
	}
}

// --- network ---

func TestNetwork_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"mainnet", false},
		{"nile", false},
		{"private", false},
		{"shasta", true},
		{"testnet", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run("network="+tc.val, func(t *testing.T) {
			yaml := []byte("name: n\nnetwork: " + tc.val + "\ntarget: {type: local}\nnodes: [{type: fullnode}]\n")
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- nodes[].type ---

func TestNodeType_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool // when missing witness_key_env for "witness"
	}{
		{"fullnode", false},
		{"solidity", false},
		{"lite", false},
		{"witness", true}, // requires witness_key_env, which we omit
		{"observer", true},
		{"", true},
	}
	for _, tc := range cases {
		t.Run("nodeType="+tc.val, func(t *testing.T) {
			yaml := []byte("name: n\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: " + tc.val + "\n")
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- nodes[].process_manager ---

func TestProcessManager_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"systemd", false},
		{"nohup", false},
		{"", false}, // omitted → default "systemd"
		{"supervisor", true},
		{"docker", true},
	}
	for _, tc := range cases {
		t.Run("pm="+tc.val, func(t *testing.T) {
			pm := ""
			if tc.val != "" {
				pm = "    process_manager: " + tc.val + "\n"
			}
			yaml := []byte("name: pm\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: fullnode\n" + pm)
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- nodes[].jvm.gc ---

func TestJVMGC_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"G1", false},
		{"CMS", false},
		{"auto", false},
		{"", false}, // omitted entirely is fine
		{"ZGC", true},
		{"Parallel", true},
	}
	for _, tc := range cases {
		t.Run("gc="+tc.val, func(t *testing.T) {
			block := ""
			if tc.val != "" {
				block = "    jvm:\n      gc: " + tc.val + "\n"
			}
			yaml := []byte("name: gc\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: fullnode\n" + block)
			_, err := Parse(yaml)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// --- nodes[].features.* ---

func TestFeatures_TriState(t *testing.T) {
	yaml := []byte(`
name: feat
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    features:
      metrics: true
      jsonrpc: false
      event_subscribe: true
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	f := i.Nodes[0].Features
	if f.Metrics == nil || !*f.Metrics {
		t.Error("metrics: true not parsed")
	}
	if f.JSONRPC == nil || *f.JSONRPC {
		t.Error("jsonrpc: false not parsed")
	}
	if f.EventSubscribe == nil || !*f.EventSubscribe {
		t.Error("event_subscribe: true not parsed")
	}
	// rate_limit was omitted; default flips it to true (per defaults.go).
	if f.RateLimit == nil || !*f.RateLimit {
		t.Error("rate_limit default true not applied")
	}
}

// --- nodes[].ports.* ---

func TestPorts_AllDefaultsApplied(t *testing.T) {
	i, err := Parse(makeIntent(""))
	if err != nil {
		t.Fatal(err)
	}
	p := i.Nodes[0].Ports
	expected := map[string]int{
		"HTTP": 8090, "GRPC": 50051, "SolidityHTTP": 8091, "SolidityGRPC": 50061,
		"JSONRPC": 8545, "P2P": 18888, "Metrics": 9527,
	}
	got := map[string]int{
		"HTTP": p.HTTP, "GRPC": p.GRPC, "SolidityHTTP": p.SolidityHTTP, "SolidityGRPC": p.SolidityGRPC,
		"JSONRPC": p.JSONRPC, "P2P": p.P2P, "Metrics": p.Metrics,
	}
	for k, want := range expected {
		if got[k] != want {
			t.Errorf("port %s default = %d, want %d", k, got[k], want)
		}
	}
}

func TestPorts_OverridesPreserved(t *testing.T) {
	yaml := []byte(`
name: po
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    ports:
      http: 19090
      grpc: 51000
      p2p: 28999
      metrics: 19527
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	p := i.Nodes[0].Ports
	if p.HTTP != 19090 || p.GRPC != 51000 || p.P2P != 28999 || p.Metrics != 19527 {
		t.Errorf("port overrides lost: %+v", p)
	}
	// Unset ones still get defaults.
	if p.SolidityHTTP != 8091 {
		t.Errorf("unset SolidityHTTP lost default: %d", p.SolidityHTTP)
	}
}

// --- nodes[].resources.memory ---

func TestResourcesMemory_DefaultAndOverride(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "16GB"},     // default
		{"32GB", "32GB"}, // explicit
		{"8G", "8G"},
		{"4096MB", "4096MB"},
	}
	for _, tc := range cases {
		t.Run("mem="+tc.input, func(t *testing.T) {
			block := ""
			if tc.input != "" {
				block = "    resources:\n      memory: " + tc.input + "\n"
			}
			yaml := []byte("name: m\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: fullnode\n" + block)
			i, err := Parse(yaml)
			if err != nil {
				t.Fatal(err)
			}
			if i.Nodes[0].Resources.Memory != tc.want {
				t.Errorf("memory = %q, want %q", i.Nodes[0].Resources.Memory, tc.want)
			}
		})
	}
}

// --- nodes[].witness_key_env (more shapes than loader_test) ---

func TestWitnessKeyEnv_AcceptedShapes(t *testing.T) {
	good := []string{"SR_KEY", "SECRET", "_PRIVATE", "K1"}
	for _, v := range good {
		if err := validateWitnessKeyEnv(v); err != nil {
			t.Errorf("expected %q valid, got %v", v, err)
		}
	}
}

func TestWitnessKeyEnv_RejectedShapes(t *testing.T) {
	bad := []struct {
		val  string
		hint string
	}{
		{"deadbeefcafebabe1234567890abcdef1234567890abcdef1234567890abcdef", "raw private key"},
		{"0xabc123", "raw private key"},
		{"0Xabc123", "raw private key"},
		{"not-an-env", "valid environment variable"},
		{"1key", "valid environment variable"},
		{" SPACED ", "valid environment variable"},
	}
	for _, tc := range bad {
		err := validateWitnessKeyEnv(tc.val)
		if err == nil {
			t.Errorf("expected %q rejected", tc.val)
			continue
		}
		if !strings.Contains(err.Error(), tc.hint) {
			t.Errorf("err for %q = %v, want hint %q", tc.val, err, tc.hint)
		}
	}
}

// --- nodes[] minimum count ---

func TestNodes_AtLeastOne(t *testing.T) {
	yaml := []byte(`
name: empty
network: mainnet
target: {type: local}
nodes: []
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Error("expected error for empty nodes")
	}
}

// --- nodes[].storage ---

func TestStorage_AllShapesParseAndPersist(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Storage
	}{
		{
			name: "default omitted",
			body: "",
			want: Storage{},
		},
		{
			name: "explicit named volumes",
			body: "    storage:\n      data: shared-data\n      logs: my-logs\n",
			want: Storage{Data: "shared-data", Logs: "my-logs"},
		},
		{
			name: "explicit bind paths",
			body: "    storage:\n      data: /var/lib/tron\n      logs: /var/log/tron\n",
			want: Storage{Data: "/var/lib/tron", Logs: "/var/log/tron"},
		},
		{
			name: "single-root path",
			body: "    storage:\n      path: /opt/tron\n",
			want: Storage{StoragePath: "/opt/tron"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaml := []byte("name: s\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: fullnode\n" + tc.body)
			i, err := Parse(yaml)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := i.Nodes[0].Storage
			if got.Data != tc.want.Data || got.Logs != tc.want.Logs || got.StoragePath != tc.want.StoragePath {
				t.Errorf("storage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// --- nodes[].restart / extra_env / extra_args / labels / resources.cpu ---

func TestRestart_Enum(t *testing.T) {
	cases := []struct {
		val     string
		wantErr bool
	}{
		{"no", false},
		{"on-failure", false},
		{"always", false},
		{"unless-stopped", false},
		{"", false},
		{"forever", true},
		{"sometimes", true},
	}
	for _, tc := range cases {
		t.Run("restart="+tc.val, func(t *testing.T) {
			body := ""
			if tc.val != "" {
				body = "    restart: " + tc.val + "\n"
			}
			y := []byte("name: r\nnetwork: mainnet\ntarget: {type: local}\nnodes:\n  - type: fullnode\n" + body)
			_, err := Parse(y)
			if tc.wantErr && err == nil {
				t.Errorf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestExtraEnv_Parses(t *testing.T) {
	y := []byte(`
name: e
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    extra_env:
      LOG_LEVEL: DEBUG
      CUSTOM: "1"
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	if i.Nodes[0].ExtraEnv["LOG_LEVEL"] != "DEBUG" || i.Nodes[0].ExtraEnv["CUSTOM"] != "1" {
		t.Errorf("extra_env mismatch: %+v", i.Nodes[0].ExtraEnv)
	}
}

func TestExtraArgs_Parses(t *testing.T) {
	y := []byte(`
name: a
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    extra_args: ["--debug", "--flag=v"]
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	if len(i.Nodes[0].ExtraArgs) != 2 || i.Nodes[0].ExtraArgs[0] != "--debug" {
		t.Errorf("extra_args mismatch: %+v", i.Nodes[0].ExtraArgs)
	}
}

func TestLabels_Parses(t *testing.T) {
	y := []byte(`
name: l
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    labels:
      role: api
      tier: edge
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	if i.Nodes[0].Labels["role"] != "api" || i.Nodes[0].Labels["tier"] != "edge" {
		t.Errorf("labels mismatch: %+v", i.Nodes[0].Labels)
	}
}

func TestResources_CPU(t *testing.T) {
	y := []byte(`
name: c
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    resources:
      cpu: "2.5"
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	if i.Nodes[0].Resources.CPU != "2.5" {
		t.Errorf("cpu = %q, want 2.5", i.Nodes[0].Resources.CPU)
	}
}

// --- network_overrides ---

func TestNetworkOverrides_Parses(t *testing.T) {
	y := []byte(`
name: no
network: private
target: {type: local}
nodes:
  - type: fullnode
    network_overrides:
      seeds: ["1.1.1.1:18888", "2.2.2.2:18888"]
      active_peers: ["3.3.3.3:18888"]
      p2p_version: 99999
      discovery: false
      max_connections: 8
      need_sync_check: false
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	no := i.Nodes[0].NetworkOverrides
	if no.Seeds == nil || len(*no.Seeds) != 2 {
		t.Errorf("seeds: %+v", no.Seeds)
	}
	if no.ActivePeers == nil || len(*no.ActivePeers) != 1 {
		t.Errorf("active_peers: %+v", no.ActivePeers)
	}
	if no.P2PVersion == nil || *no.P2PVersion != 99999 {
		t.Errorf("p2p_version: %+v", no.P2PVersion)
	}
	if no.Discovery == nil || *no.Discovery {
		t.Errorf("discovery: %+v", no.Discovery)
	}
	if no.MaxConnections == nil || *no.MaxConnections != 8 {
		t.Errorf("max_connections: %+v", no.MaxConnections)
	}
	if no.NeedSyncCheck == nil || *no.NeedSyncCheck {
		t.Errorf("need_sync_check: %+v", no.NeedSyncCheck)
	}
}

// --- witness_key (structured) ---

func TestWitnessKey_StructuredAccepted(t *testing.T) {
	y := []byte(`
name: wk
network: private
target: {type: local}
nodes:
  - type: witness
    witness_key:
      private_key_env: SR_KEY
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if i.Nodes[0].WitnessKey == nil || i.Nodes[0].WitnessKey.PrivateKeyEnv != "SR_KEY" {
		t.Errorf("witness_key: %+v", i.Nodes[0].WitnessKey)
	}
}

func TestWitnessKey_RawHexInPrivateKeyEnvRejected(t *testing.T) {
	y := []byte(`
name: bad
network: private
target: {type: local}
nodes:
  - type: witness
    witness_key:
      private_key_env: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`)
	_, err := Parse(y)
	if err == nil {
		t.Fatal("expected raw-hex private_key_env to be rejected")
	}
	if !strings.Contains(err.Error(), "raw private key") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWitness_NeedsAtLeastOneKeySource(t *testing.T) {
	y := []byte(`
name: nokey
network: private
target: {type: local}
nodes:
  - type: witness
`)
	_, err := Parse(y)
	if err == nil {
		t.Fatal("expected witness with no key to be rejected")
	}
}

// --- config_overrides ---

func TestConfigOverrides_Parses(t *testing.T) {
	y := []byte(`
name: co
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    config_overrides:
      "vm.supportConstant": true
      "block.maintenanceTimeInterval": 30000
      "storage.db.engine": "ROCKSDB"
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	co := i.Nodes[0].ConfigOverrides
	if co["vm.supportConstant"] != true {
		t.Errorf("vm.supportConstant: %v", co["vm.supportConstant"])
	}
	if co["storage.db.engine"] != "ROCKSDB" {
		t.Errorf("storage.db.engine: %v", co["storage.db.engine"])
	}
}

// --- compose-only fields parse ---

func TestComposeFields_Parse(t *testing.T) {
	y := []byte(`
name: cf
network: mainnet
target: {type: local}
nodes:
  - type: fullnode
    networks: [tron-mesh]
    depends_on: [seed-node]
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://localhost:8090/"]
      interval: 5s
      retries: 3
    ulimits: {nofile: 65535}
    extra_hosts: {peer1: 10.0.0.1}
    entrypoint: ["/bin/sh", "-c"]
    logging:
      driver: json-file
      options: {max-size: "100m"}
    shm_size: 64m
`)
	i, err := Parse(y)
	if err != nil {
		t.Fatal(err)
	}
	n := i.Nodes[0]
	if len(n.Networks) != 1 || n.Networks[0] != "tron-mesh" {
		t.Errorf("networks: %+v", n.Networks)
	}
	if len(n.DependsOn) != 1 || n.DependsOn[0] != "seed-node" {
		t.Errorf("depends_on: %+v", n.DependsOn)
	}
	if n.Healthcheck == nil || len(n.Healthcheck.Test) != 4 || n.Healthcheck.Interval != "5s" {
		t.Errorf("healthcheck: %+v", n.Healthcheck)
	}
	if n.Ulimits == nil || n.Ulimits.NOFile != 65535 {
		t.Errorf("ulimits: %+v", n.Ulimits)
	}
	if n.ExtraHosts["peer1"] != "10.0.0.1" {
		t.Errorf("extra_hosts: %+v", n.ExtraHosts)
	}
	if len(n.Entrypoint) != 2 {
		t.Errorf("entrypoint: %+v", n.Entrypoint)
	}
	if n.Logging == nil || n.Logging.Driver != "json-file" {
		t.Errorf("logging: %+v", n.Logging)
	}
	if n.ShmSize != "64m" {
		t.Errorf("shm_size: %q", n.ShmSize)
	}
}

// --- multi-node intent parses ---

func TestNodes_Multi(t *testing.T) {
	yaml := []byte(`
name: multi
network: private
target: {type: local}
nodes:
  - type: fullnode
  - type: fullnode
  - type: lite
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if len(i.Nodes) != 3 {
		t.Errorf("got %d nodes, want 3", len(i.Nodes))
	}
}

// --- monitoring ---

func TestMonitoring_ParsesAndDefaults(t *testing.T) {
	yaml := []byte(`
name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  prometheus:
    port: 19090
    retention: 14d
  grafana:
    port: 13000
nodes:
  - type: fullnode
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if i.Monitoring == nil || i.Monitoring.Enabled == nil || !*i.Monitoring.Enabled {
		t.Fatal("monitoring not parsed")
	}
	if i.Monitoring.Prometheus.Port != 19090 {
		t.Errorf("prometheus port = %d, want 19090", i.Monitoring.Prometheus.Port)
	}
	if i.Monitoring.Grafana.Port != 13000 {
		t.Errorf("grafana port = %d, want 13000", i.Monitoring.Grafana.Port)
	}
	if i.Monitoring.Prometheus.Retention != "14d" {
		t.Errorf("retention = %q, want 14d", i.Monitoring.Prometheus.Retention)
	}
}

func TestMonitoring_DefaultsWhenOmitted(t *testing.T) {
	i, err := Parse(makeIntent(""))
	if err != nil {
		t.Fatal(err)
	}
	if i.Monitoring != nil {
		t.Errorf("monitoring should be nil when omitted, got %+v", i.Monitoring)
	}
}

func TestMonitoring_DefaultsWhenEmpty(t *testing.T) {
	yaml := []byte(`
name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
nodes:
  - type: fullnode
`)
	i, err := Parse(yaml)
	if err != nil {
		t.Fatal(err)
	}
	if i.Monitoring.Prometheus.Port != 0 {
		t.Errorf("prometheus port default = %d, want 0 (unexposed)", i.Monitoring.Prometheus.Port)
	}
	if i.Monitoring.Grafana.Port != 3000 {
		t.Errorf("grafana port default = %d, want 3000", i.Monitoring.Grafana.Port)
	}
	if i.Monitoring.Prometheus.Retention != "7d" {
		t.Errorf("retention default = %q, want 7d", i.Monitoring.Prometheus.Retention)
	}
}

func TestMonitoring_InvalidPort(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			"prometheus.port=80",
			`name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  prometheus:
    port: 80
nodes:
  - type: fullnode
`,
		},
		{
			"prometheus.port=70000",
			`name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  prometheus:
    port: 70000
nodes:
  - type: fullnode
`,
		},
		{
			"grafana.port=443",
			`name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  grafana:
    port: 443
nodes:
  - type: fullnode
`,
		},
		{
			"grafana.port=99999",
			`name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  grafana:
    port: 99999
nodes:
  - type: fullnode
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestMonitoring_GrafanaExposeAndAdminPassword(t *testing.T) {
	monitoringYAML := func(grafana string) []byte {
		return []byte(fmt.Sprintf(`
name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  grafana:
%s
nodes:
  - type: fullnode
`, grafana))
	}

	t.Run("defaults to not exposed", func(t *testing.T) {
		i, err := Parse(monitoringYAML("    port: 3000"))
		if err != nil {
			t.Fatal(err)
		}
		if i.Monitoring.Grafana.Expose {
			t.Error("grafana.expose should default to false")
		}
		if i.Monitoring.Grafana.AdminPasswordEnv != "" {
			t.Errorf("grafana.admin_password_env = %q, want empty",
				i.Monitoring.Grafana.AdminPasswordEnv)
		}
	})

	t.Run("expose with admin_password_env parses", func(t *testing.T) {
		i, err := Parse(monitoringYAML("    expose: true\n    admin_password_env: GRAFANA_ADMIN_PASSWORD"))
		if err != nil {
			t.Fatal(err)
		}
		if !i.Monitoring.Grafana.Expose {
			t.Error("grafana.expose not parsed")
		}
		if i.Monitoring.Grafana.AdminPasswordEnv != "GRAFANA_ADMIN_PASSWORD" {
			t.Errorf("grafana.admin_password_env = %q, want GRAFANA_ADMIN_PASSWORD",
				i.Monitoring.Grafana.AdminPasswordEnv)
		}
	})

	// Exposing Grafana on every interface with the image's default
	// admin login is the combination this rejects.
	t.Run("expose without admin_password_env rejected", func(t *testing.T) {
		_, err := Parse(monitoringYAML("    expose: true"))
		if err == nil {
			t.Fatal("expected error for expose without admin_password_env")
		}
		if !strings.Contains(err.Error(), "admin_password_env") {
			t.Errorf("error %q should name the missing field", err)
		}
	})

	// The intent carries the env var NAME, never the password.
	t.Run("literal password rejected", func(t *testing.T) {
		_, err := Parse(monitoringYAML(`    admin_password_env: "hunter2 !"`))
		if err == nil {
			t.Fatal("expected error for a non-env-name admin_password_env")
		}
	})
}

func TestMonitoring_InvalidRetention(t *testing.T) {
	cases := []string{"7", "days", "1.5d", "week"}
	for _, val := range cases {
		t.Run("retention="+val, func(t *testing.T) {
			yaml := fmt.Sprintf(`
name: mon
network: mainnet
target: {type: local}
monitoring:
  enabled: true
  prometheus:
    retention: %s
nodes:
  - type: fullnode
`, val)
			_, err := Parse([]byte(yaml))
			if err == nil {
				t.Errorf("expected error for retention=%q", val)
			}
		})
	}
}

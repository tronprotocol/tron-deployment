package render

import (
	"slices"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestLoadTemplate_EmbeddedFallback(t *testing.T) {
	cases := []string{"mainnet", "nile", "private"}
	for _, net := range cases {
		data, err := LoadTemplate("", net)
		if err != nil {
			t.Errorf("LoadTemplate(%q) failed: %v", net, err)
			continue
		}
		if len(data) < 100 {
			t.Errorf("LoadTemplate(%q) returned suspiciously small payload (%d bytes)", net, len(data))
		}
	}
}

func TestLoadTemplate_UnknownNetwork(t *testing.T) {
	if _, err := LoadTemplate("", "bogus"); err == nil {
		t.Error("expected error for unknown network")
	}
}

func TestRenderHOCON_PortOverrides(t *testing.T) {
	i := &intent.Intent{
		Name:    "port-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "fullnode",
		Ports: intent.PortMapping{
			HTTP: 19090,
			P2P:  28888,
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, "fullNodePort = 19090") {
		t.Error("HTTP port override not applied")
	}
	if !strings.Contains(out, "listen.port = 28888") {
		t.Error("P2P port override not applied")
	}
}

func TestRenderHOCON_HTTPPortCollisionAvoidance(t *testing.T) {
	base := &intent.Intent{Name: "ports", Network: "mainnet", Target: intent.Target{Type: "local"}}
	for _, tt := range []struct {
		name       string
		http       int
		messageHas string
	}{{"solidity default collision", 8091, "ports.solidity_http"}, {"PBFT default collision", 8092, "node.http.PBFTPort"}} {
		t.Run(tt.name, func(t *testing.T) {
			node := &intent.NodeSpec{Type: "fullnode", Ports: intent.PortMapping{HTTP: tt.http}}
			_, err := RenderHOCON("", base, node)
			if err == nil || !strings.Contains(err.Error(), tt.messageHas) || !strings.Contains(err.Error(), "different port") {
				t.Fatalf("expected actionable collision error, got %v", err)
			}
		})
	}
}

func TestRenderHOCON_HTTPPortCollisionExplicitSolidity(t *testing.T) {
	// config_overrides is the explicit escape hatch for operators who
	// intentionally need the equal value.
	node := &intent.NodeSpec{Type: "fullnode", Ports: intent.PortMapping{HTTP: 8091, SolidityHTTP: 8091}}
	_, err := RenderHOCON("", &intent.Intent{Name: "ports", Network: "mainnet"}, node)
	if err == nil || !strings.Contains(err.Error(), "solidityPort") {
		t.Fatalf("expected solidity collision error, got %v", err)
	}

	node.ConfigOverrides = map[string]any{"node.http.solidityPort": 8091}
	out, err := RenderHOCON("", &intent.Intent{Name: "ports", Network: "mainnet"}, node)
	if err != nil {
		t.Fatalf("render with override: %v", err)
	}
	if !strings.Contains(out, "solidityPort = 8091") {
		t.Error("config_overrides solidity port escape hatch was changed")
	}
}

func TestRenderHOCON_HTTPPortCollisionThroughIntentParse(t *testing.T) {
	data := []byte(`name: pipeline-port-test
target:
  type: local
  runtime: docker
network: mainnet
nodes:
  - type: fullnode
    version: latest
    ports:
      http: 8091
`)
	i, err := intent.Parse(data)
	if err != nil {
		t.Fatalf("parse intent: %v", err)
	}
	if i.Nodes[0].Ports.SolidityHTTP != 8091 {
		t.Fatalf("ApplyDefaults did not fill solidity HTTP port: %d", i.Nodes[0].Ports.SolidityHTTP)
	}

	if _, err := RenderHOCON("", i, &i.Nodes[0]); err == nil || !strings.Contains(err.Error(), "solidityPort") {
		t.Fatalf("expected parsed collision error, got %v", err)
	}
}

func TestHTTPPortCollisionCheck(t *testing.T) {
	config := "fullNodePort = 65534\nsolidityPort = 65534\nPBFTPort = 65534\n"
	node := &intent.NodeSpec{Type: "fullnode"}
	if err := checkHTTPPortConflicts(config, node); err == nil {
		t.Fatal("expected collision error")
	}
	if err := checkHTTPPortConflicts("fullNodePort = 8090\n", node); err != nil {
		t.Fatalf("missing service keys should not error: %v", err)
	}
	if err := checkHTTPPortConflicts(config, &intent.NodeSpec{ConfigOverrides: map[string]any{
		"node.http.solidityPort": 65534,
		"node.http.PBFTPort":     65534,
	}}); err != nil {
		t.Fatalf("explicit override should suppress error: %v", err)
	}
}

func TestRenderHOCON_DefaultHTTPPortsUnchanged(t *testing.T) {
	out, err := RenderHOCON("", &intent.Intent{Name: "ports", Network: "mainnet"}, &intent.NodeSpec{Type: "fullnode"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"fullNodePort = 8090", "solidityPort = 8091", "PBFTPort = 8092"} {
		if !strings.Contains(out, want) {
			t.Errorf("default port missing or changed: %s", want)
		}
	}
}

func TestRenderHOCON_SolidityGRPCPortOverride(t *testing.T) {
	i := &intent.Intent{Name: "grpc", Network: "mainnet"}
	n := &intent.NodeSpec{Type: "fullnode", Ports: intent.PortMapping{SolidityGRPC: 56061}}
	out, err := RenderHOCONWithSecrets("", i, n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Deployable(), "solidityPort = 56061") {
		t.Fatalf("solidity gRPC port not rendered: %s", out.Deployable())
	}
}

func TestRenderHOCON_SolidityPBFTCollision(t *testing.T) {
	n := &intent.NodeSpec{Type: "fullnode", Ports: intent.PortMapping{HTTP: 8091}}
	i := &intent.Intent{Name: "collision", Network: "mainnet"}
	_, err := RenderHOCON("", i, n)
	if err == nil || !strings.Contains(err.Error(), "solidityPort") {
		t.Fatalf("expected collision, got %v", err)
	}
}

func TestRenderHOCON_SolidityPBFTDirectCollision(t *testing.T) {
	n := &intent.NodeSpec{Type: "fullnode", Ports: intent.PortMapping{SolidityHTTP: 8092}}
	i := &intent.Intent{Name: "direct-collision", Network: "mainnet"}
	_, err := RenderHOCON("", i, n)
	if err == nil || !strings.Contains(err.Error(), "solidityPort") || !strings.Contains(err.Error(), "PBFTPort") {
		t.Fatalf("expected solidity/PBFT collision, got %v", err)
	}
}

// TestRenderHOCON_JSONRPCPortAndEnable locks down the #165 fix:
// features.jsonrpc=true + ports.jsonrpc=NNNNN must produce BOTH
// `httpFullNodeEnable = true` (already worked) AND
// `httpFullNodePort = NNNNN` (the bug — was commented out in
// template and never substituted, so java-tron fell back to
// internal default 8545 while docker bound the intent port).
func TestRenderHOCON_JSONRPCPortAndEnable(t *testing.T) {
	enabled := true
	i := &intent.Intent{
		Name:    "jsonrpc-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "fullnode",
		Features: intent.Features{
			JSONRPC: &enabled,
		},
		Ports: intent.PortMapping{
			JSONRPC: 58545,
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, "httpFullNodeEnable = true") {
		t.Error("JSONRPC enable bit not written")
	}
	if !strings.Contains(out, "httpFullNodePort = 58545") {
		t.Errorf("JSONRPC port not wired into HOCON (#165 regression)")
	}
	// The template ships the line commented out; verify we don't have
	// BOTH the commented default AND the active override side by side.
	if strings.Contains(out, "# httpFullNodePort = 8545") &&
		strings.Contains(out, "httpFullNodePort = 58545") {
		t.Error("both commented template line and override present — render replaced wrong line")
	}
}

// TestRenderHOCON_ShadowForkRocksIntent mirrors the actual rocksdb
// shadow-fork e2e intent used on 2026-05-26 (qemu arm64 run that
// proved #166). Locks down that the WHOLE intent shape — features +
// ports + config_overrides + storage.db.engine — renders into a HOCON
// where every required wiring is present, WITHOUT needing operators
// to use config_overrides as a workaround for #165.
//
// The historical bug was: features.jsonrpc=true plus ports.jsonrpc set,
// but operators STILL had to add `"node.jsonrpc.httpFullNodePort": N`
// to config_overrides because the port wasn't propagating. After #165
// the port flows from the intent's ports.jsonrpc field directly.
func TestRenderHOCON_ShadowForkRocksIntent(t *testing.T) {
	enabled := true
	i := &intent.Intent{
		Name:    "shadow-fork-rocks",
		Network: "nile",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "witness",
		Features: intent.Features{
			JSONRPC: &enabled,
			Metrics: &enabled,
		},
		Ports: intent.PortMapping{
			HTTP:    58090,
			GRPC:    60051,
			JSONRPC: 58545,
			P2P:     58888,
			Metrics: 59527,
		},
		ConfigOverrides: map[string]any{
			"seed.node.ip.list":         []any{},
			"node.p2p.version":          99999,
			"node.minParticipationRate": 0,
			"storage.db.engine":         "ROCKSDB",
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Every port from the intent that has a slot in the Nile template
	// must land in the HOCON. NOTE: the Nile template (test_net_config
	// .conf) lacks a `node.metrics.prometheus` block entirely, so
	// metrics-on-nile is a separate gap not in scope here — tracked
	// elsewhere. JSON-RPC + HTTP + P2P all have slots and must wire.
	wants := map[string]string{
		"http (node.http.fullNodePort)":             "fullNodePort = 58090",
		"jsonrpc (node.jsonrpc.httpFullNodeEnable)": "httpFullNodeEnable = true",
		"jsonrpc (node.jsonrpc.httpFullNodePort)":   "httpFullNodePort = 58545",
		"p2p (node.listen.port)":                    "listen.port = 58888",
		"storage.db.engine override (rocksdb path)": "ROCKSDB",
	}
	for label, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing: expected %q in rendered HOCON", label, want)
		}
	}

	// Negative assertion: the operator-workaround used during the
	// May 25 e2e session — explicit `"node.jsonrpc.httpFullNodePort"`
	// in config_overrides — must not be REQUIRED for the port to land.
	// We deliberately do NOT set that key in ConfigOverrides above; if
	// the port still appears, #165 is fixed for the rocksdb shape too.
	if !strings.Contains(out, "httpFullNodePort = 58545") {
		t.Error("#165 regression: jsonrpc port absent despite ports.jsonrpc=58545 — " +
			"operators would need a config_overrides workaround to ship rocksdb e2e")
	}
}

// TestRenderHOCON_MetricsPort exercises the parallel fix for the
// metrics endpoint. Same shape as JSONRPC: template ships with port
// = 9527 as default; intent override must propagate.
func TestRenderHOCON_MetricsPort(t *testing.T) {
	i := &intent.Intent{
		Name:    "metrics-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type:  "fullnode",
		Ports: intent.PortMapping{Metrics: 59527},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// The prometheus block is the only one with bare `port = N` at
	// metrics-level nesting; the value must be the override, not 9527.
	if !strings.Contains(out, "port = 59527") {
		t.Errorf("metrics port not wired into HOCON")
	}
	if strings.Contains(out, "port = 9527") {
		t.Errorf("default metrics port not replaced")
	}
}

// prometheusBlock extracts the `prometheus { ... }` sub-block from a
// rendered config so assertions can be scoped to it (the full config
// has many unrelated enable = false lines).
func prometheusBlock(t *testing.T, config string) string {
	t.Helper()
	lines := strings.Split(config, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "prometheus") && strings.Contains(l, "{") {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "}" {
					return strings.Join(lines[i:j+1], "\n")
				}
			}
		}
	}
	t.Fatalf("no prometheus block found in rendered config")
	return ""
}

// TestRenderHOCON_MetricsFeatureEnables locks the metrics-enable fix
// (symmetric to #165): features.metrics=true must flip the mainnet
// template's prometheus.enable from false to true, otherwise the bound
// docker port serves nothing. Also asserts the Nile template (no
// prometheus block, #167) is a safe no-op — render must not corrupt it.
func TestRenderHOCON_MetricsFeatureEnables(t *testing.T) {
	enabled := true

	t.Run("mainnet flips enable=true", func(t *testing.T) {
		out, err := RenderHOCON("", &intent.Intent{
			Name: "m", Network: "mainnet", Target: intent.Target{Type: "local"},
		}, &intent.NodeSpec{
			Type:     "fullnode",
			Features: intent.Features{Metrics: &enabled},
			Ports:    intent.PortMapping{Metrics: 59527},
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		// Scope the assertion to the prometheus block specifically — the
		// mainnet config has several other `enable = false` lines
		// (influxdb, other features) that this fix must NOT touch.
		prom := prometheusBlock(t, out)
		if !strings.Contains(prom, "enable = true") {
			t.Errorf("prometheus.enable not flipped to true; block was:\n%s", prom)
		}
		if strings.Contains(prom, "enable = false") {
			t.Errorf("prometheus.enable left as false despite features.metrics=true; block:\n%s", prom)
		}
	})

	// Since #167 the nile and private templates ship the same
	// node.metrics.prometheus block as mainnet (enable = false by
	// default), so features.metrics flips them on too — the feature is
	// no longer a silent no-op on those networks.
	for _, network := range []string{"nile", "private"} {
		t.Run(network+" flips enable=true", func(t *testing.T) {
			out, err := RenderHOCON("", &intent.Intent{
				Name: "m", Network: network, Target: intent.Target{Type: "local"},
			}, &intent.NodeSpec{
				Type:     "fullnode",
				Features: intent.Features{Metrics: &enabled},
				Ports:    intent.PortMapping{Metrics: 59527},
			})
			if err != nil {
				t.Fatalf("render %s: %v", network, err)
			}
			prom := prometheusBlock(t, out)
			if !strings.Contains(prom, "enable = true") {
				t.Errorf("%s prometheus.enable not flipped to true; block was:\n%s", network, prom)
			}
			if strings.Contains(prom, "enable = false") {
				t.Errorf("%s prometheus.enable left false despite features.metrics=true; block:\n%s", network, prom)
			}
		})
	}

	t.Run("default off when metrics feature absent", func(t *testing.T) {
		// Without features.metrics the template default must stand —
		// prometheus block present (so monitoring/features can later flip
		// it) but enable = false, so we never expose /metrics unasked.
		out, err := RenderHOCON("", &intent.Intent{
			Name: "m", Network: "nile", Target: intent.Target{Type: "local"},
		}, &intent.NodeSpec{Type: "fullnode"})
		if err != nil {
			t.Fatalf("render nile: %v", err)
		}
		prom := prometheusBlock(t, out)
		if !strings.Contains(prom, "enable = false") {
			t.Errorf("nile prometheus.enable should default to false; block:\n%s", prom)
		}
	})
}

// TestRenderHOCON_MetricsAndMonitoringIdempotent pins that setting BOTH
// features.metrics and monitoring.enabled yields a SINGLE
// node.metrics.prometheus block with enable=true — never a duplicate.
// RenderHOCON runs ensureMetricsEnabled (features path) and then
// ensureMetricsForMonitoring (monitoring path) over the same config; now
// that every template ships the block (#167) both just flip the existing
// `enable` line, so they compose idempotently. This guards the
// interaction across the planned merge of the two functions into one.
func TestRenderHOCON_MetricsAndMonitoringIdempotent(t *testing.T) {
	en := true
	for _, network := range []string{"nile", "private", "mainnet"} {
		t.Run(network, func(t *testing.T) {
			out, err := RenderHOCON("", &intent.Intent{
				Name: "m", Network: network, Target: intent.Target{Type: "local"},
				Monitoring: &intent.Monitoring{Enabled: &en},
			}, &intent.NodeSpec{
				Type:     "fullnode",
				Features: intent.Features{Metrics: &en},
				Ports:    intent.PortMapping{Metrics: 59527},
			})
			if err != nil {
				t.Fatalf("render %s: %v", network, err)
			}
			if n := strings.Count(out, "node.metrics"); n != 1 {
				t.Errorf("%s: want exactly one node.metrics block, got %d", network, n)
			}
			if n := strings.Count(out, "prometheus {"); n != 1 {
				t.Errorf("%s: want exactly one prometheus block, got %d", network, n)
			}
			prom := prometheusBlock(t, out)
			if !strings.Contains(prom, "enable = true") {
				t.Errorf("%s: prometheus.enable not true with both flags set; block:\n%s", network, prom)
			}
		})
	}
}

func TestRenderHOCON_UnknownNetwork(t *testing.T) {
	i := &intent.Intent{Network: "martian"}
	node := &intent.NodeSpec{Type: "fullnode"}
	if _, err := RenderHOCON("", i, node); err == nil {
		t.Error("expected error for unknown network")
	}
}

func TestReplaceHOCONValue(t *testing.T) {
	in := `  fullNodePort = 8090
  other = 1`
	out := replaceHOCONValue(in, "fullNodePort", "9999")
	if !strings.Contains(out, "fullNodePort = 9999") {
		t.Errorf("replacement failed: %s", out)
	}
	// Indentation is preserved
	if !strings.Contains(out, "  fullNodePort = 9999") {
		t.Errorf("indentation lost: %q", out)
	}
}

func TestEnsureMetricsForMonitoring_ExistingEnableFalse(t *testing.T) {
	in := `node.metrics {
  prometheus {
    enable = false
    port = 9527
  }
}`
	out := ensureMetricsForMonitoring(in)
	if !strings.Contains(out, "enable = true") {
		t.Errorf("expected enable = true, got:\n%s", out)
	}
}

func TestEnsureMetricsForMonitoring_NoEnableField(t *testing.T) {
	in := `node.metrics {
  prometheus {
    port = 9527
  }
}`
	out := ensureMetricsForMonitoring(in)
	if !strings.Contains(out, "enable = true") {
		t.Errorf("expected enable = true inserted, got:\n%s", out)
	}
}

func TestEnsureMetricsForMonitoring_NoPrometheusBlock(t *testing.T) {
	in := `node.metrics {
  something = else
}`
	out := ensureMetricsForMonitoring(in)
	if !strings.Contains(out, "prometheus {") || !strings.Contains(out, "enable = true") {
		t.Errorf("expected prometheus block inserted, got:\n%s", out)
	}
}

func TestEnsureMetricsForMonitoring_MissingMetricsBlock(t *testing.T) {
	in := `some.config = value`
	out := ensureMetricsForMonitoring(in)
	if !strings.Contains(out, "node.metrics {") || !strings.Contains(out, "prometheus {") {
		t.Errorf("expected full metrics block appended, got:\n%s", out)
	}
}

func TestRenderHOCON_MonitoringEnabled(t *testing.T) {
	i := &intent.Intent{
		Name:       "mon-test",
		Network:    "mainnet",
		Target:     intent.Target{Type: "local"},
		Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(true)},
	}
	node := &intent.NodeSpec{Type: "fullnode"}
	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "node.metrics") {
		t.Error("missing node.metrics block")
	}
	if !strings.Contains(out, "enable = true") {
		t.Error("metrics not enabled")
	}
}

func TestRenderHOCON_MonitoringDisabled(t *testing.T) {
	i := &intent.Intent{
		Name:    "no-mon",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{Type: "fullnode"}
	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// When monitoring is not enabled, metrics block should NOT be injected.
	// Template may already contain node.metrics block with enable=false.
	// When monitoring is disabled, we should NOT see enable=true.
	lines := strings.Split(out, "\n")
	inMetrics := false
	inPrometheus := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "node.metrics") && strings.Contains(trimmed, "{") {
			inMetrics = true
			continue
		}
		if inMetrics && strings.HasPrefix(trimmed, "prometheus") && strings.Contains(trimmed, "{") {
			inPrometheus = true
			continue
		}
		if inPrometheus && strings.Contains(trimmed, "enable = true") {
			t.Error("metrics enable=true should not be present when monitoring is disabled")
		}
		if inPrometheus && trimmed == "}" {
			break
		}
	}
}

// A localwitness array whose key sits on its own line must not survive
// redaction: only the opening line starts with the key name, so a
// per-line pass used to let the key itself straight through into
// `plan --diff`, `config diff`, `verify-config` and the MCP drift tool.
func TestRedactWitnessLinesMultiLineArray(t *testing.T) {
	const key = "a31d54825aea2fc5127e3bd435fc2346021313005e5f304ab33372432784acae"
	in := []string{
		"storage = {",
		"localwitness = [",
		"  " + key + "  # you must enable this value",
		"]",
		"block = {",
	}
	got := RedactWitnessLines(in)
	if len(got) != len(in) {
		t.Fatalf("length changed: got %d want %d", len(got), len(in))
	}
	for i, line := range got {
		if strings.Contains(line, key) {
			t.Errorf("line %d leaked the witness key: %q", i, line)
		}
	}
	if got[0] != in[0] || got[4] != in[4] {
		t.Errorf("unrelated lines were rewritten: %q %q", got[0], got[4])
	}
	if got[3] != "]" {
		t.Errorf("closing bracket rewritten: %q", got[3])
	}
}

// The single-line form keeps behaving exactly as before.
func TestRedactWitnessLinesSingleLine(t *testing.T) {
	in := []string{`localwitness = ["deadbeef"]`}
	got := RedactWitnessLines(in)
	if strings.Contains(got[0], "deadbeef") {
		t.Fatalf("single-line key leaked: %q", got[0])
	}
}

// Redaction must not depend on line shape. These are the shapes a scan
// over brackets gets wrong: a `]` inside the comment on the opening
// line ends the array before it began, and an element that closes the
// array leaves the scanner inside it for the rest of the file.
func TestRedactWitnessLinesIgnoresLineShape(t *testing.T) {
	const key = "da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0"

	cases := map[string][]string{
		"comment carries a closing bracket": {
			`localwitness = [ # note the ] in this comment`,
			`  ` + key,
			`]`,
			`block = {`,
		},
		"element closes the array": {
			`localwitness = [`,
			`  ` + key + `]`,
			`node.p2p.version = 11111`,
		},
		"single line": {`localwitness = ["` + key + `"]`},
		"multi line":  {`localwitness = [`, `  ` + key + `  # matched`, `]`},
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := RedactWitnessLines(in)
			if len(got) != len(in) {
				t.Fatalf("length changed: got %d want %d", len(got), len(in))
			}
			for i, line := range got {
				if strings.Contains(line, key) {
					t.Errorf("line %d leaked the key: %q", i, line)
				}
			}
		})
	}
}

// Over-redaction is its own failure: a diff whose unrelated lines all
// read <REDACTED> tells the reader nothing.
func TestRedactWitnessLinesLeavesOtherKeysAlone(t *testing.T) {
	const key = "da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0"
	in := []string{
		`localwitness = [`,
		`  ` + key + `]`,
		`node.p2p.version = 11111`,
		`storage.db.directory = "database"`,
	}
	got := RedactWitnessLines(in)
	for _, want := range []string{`node.p2p.version = 11111`, `storage.db.directory = "database"`} {
		if !slices.Contains(got, want) {
			t.Errorf("unrelated line was rewritten; wanted %q in %q", want, got)
		}
	}
}

// Text that does not parse still has to be redacted — that is the whole
// point of keeping the scan.
func TestRedactWitnessLinesFallsBackOnUnparseableText(t *testing.T) {
	const key = "da146374a75310b9666e834ee4ad0866d6f4035967bfc76217c5a495fff9f0d0"
	in := []string{
		`this { is not : valid ] hocon [`,
		`localwitness = [`,
		`  ` + key,
		`]`,
	}
	for i, line := range RedactWitnessLines(in) {
		if strings.Contains(line, key) {
			t.Errorf("line %d leaked the key on the fallback path: %q", i, line)
		}
	}
}

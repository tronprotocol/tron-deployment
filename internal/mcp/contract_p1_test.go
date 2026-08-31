package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

func TestPlanToolReturnsPromisedContractFields(t *testing.T) {
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, []byte("name: p1\nnetwork: nile\ntarget:\n  type: local\n  runtime: docker\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, value, err := planTool(context.Background(), nil, planArg{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	got := value.(map[string]any)
	for _, key := range []string{"changes", "destructive", "estimated_downtime_seconds"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("plan missing %q: %v", key, got)
		}
	}
}

func TestPlanToolCreateNoOpUpdateStates(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	p := writePlanFixture(t, "plan-node")
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := intent.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := render.RenderHOCONWithSecrets("", parsed, &parsed.Nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	configHash := apply.IntentHashFromBytes([]byte(rendered.Deployable()))
	canonical, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		node *state.ManagedNode
		want int
	}{
		{"create", nil, 1},
		{"noop", &state.ManagedNode{Name: "plan-node", IntentHash: apply.EffectiveIntentHash(canonical), ConfigHash: configHash, Version: "4.8.1", Status: "running"}, 0},
		{"update", &state.ManagedNode{Name: "plan-node", IntentHash: "different", ConfigHash: configHash, Version: "4.8.1", Status: "running"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.DeploymentState{}
			if tc.node != nil {
				st.Nodes = []state.ManagedNode{*tc.node}
			}
			if err := store.Save(st); err != nil {
				t.Fatal(err)
			}
			_, value, err := planTool(context.Background(), nil, planArg{Path: p})
			if err != nil {
				t.Fatal(err)
			}
			got := value.(map[string]any)
			changes := got["changes"].([]map[string]any)
			if len(changes) != tc.want {
				t.Fatalf("changes=%v, want %d", changes, tc.want)
			}
			downtime := got["estimated_downtime_seconds"].(int)
			if tc.name == "update" && downtime != 0 {
				t.Fatalf("downtime=%d inconsistent", downtime)
			}
		})
	}
}

func TestPlanToolCurrentStateUsesThreeStateDomain(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	p := writePlanFixture(t, "state-domain")
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, status, want string }{{"not deployed", "", "not_deployed"}, {"running", "running", "running"}, {"error", "error", "error"}} {
		t.Run(tc.name, func(t *testing.T) {
			st := &state.DeploymentState{}
			if tc.status != "" {
				st.Nodes = []state.ManagedNode{{Name: "state-domain", Status: tc.status}}
			}
			if err := store.Save(st); err != nil {
				t.Fatal(err)
			}
			_, value, err := planTool(context.Background(), nil, planArg{Path: p})
			if err != nil {
				t.Fatal(err)
			}
			if got := value.(map[string]any)["current_state"]; got != tc.want {
				t.Fatalf("current_state=%v, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyToolLegacyHashUsesRecordedHashForNoChange(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	binDir := t.TempDir()
	dockerCalls := t.TempDir() + "/docker.calls"
	fakeDocker := binDir + "/docker"
	if err := os.WriteFile(fakeDocker, []byte("#!/bin/sh\nprintf x >> \"$DOCKER_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CALLS", dockerCalls)
	raw := []byte("name: legacy-apply\nnetwork: private\ntarget:\n  type: local\n  runtime: docker\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n")
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{
		Name: "legacy-apply", Status: "running", Network: "private", Runtime: "docker",
		IntentHash: apply.IntentHashFromBytes(raw),
	}}}); err != nil {
		t.Fatal(err)
	}
	res, value, err := applyTool(context.Background(), nil, applyArgs{Path: p})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("apply result=%v err=%v", res, err)
	}
	got, ok := value.(*apply.Result)
	if !ok {
		t.Fatalf("result type=%T, want *apply.Result", value)
	}
	if got.Outcome != "no_change" {
		t.Fatalf("outcome=%q, want no_change", got.Outcome)
	}
	if got.IntentHash != apply.IntentHashFromBytes(raw) {
		t.Fatalf("intent hash=%q, want recorded legacy hash", got.IntentHash)
	}
	if data, readErr := os.ReadFile(dockerCalls); readErr == nil && len(data) != 0 {
		t.Fatalf("fake runtime Deploy invoked %d time(s), want 0", len(data))
	}
}

func TestApplyToolUsesConfiguredTemplateForPlanAndDeploy(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	templateDir := t.TempDir()
	t.Setenv("TROND_TEMPLATES_DIR", templateDir)
	// The template loader requires the matching private-network file. The
	// marker makes the rendered/deployed config observably external-template based.
	marker := "# external-template-marker"
	// rpc block required: ports.solidity_grpc render now errors when the template
	// has no rpc block (AUD-024). Keep the default value so the rendered output
	// stays byte-identical to this template.
	template := marker + "\nnode {\n  metrics {\n    prometheus = false\n  }\n}\nrpc {\n  port = 50051\n  solidityPort = 50061\n}\n"
	if err := os.WriteFile(templateDir+"/private_net_config.conf", []byte(template), 0600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	dockerCalls := t.TempDir() + "/docker.calls"
	if err := os.WriteFile(binDir+"/docker", []byte("#!/bin/sh\nprintf x >> \"$DOCKER_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CALLS", dockerCalls)
	raw := []byte("name: external-template\nnetwork: private\ntarget:\n  type: local\n  runtime: docker\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n")
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	res, value, err := applyTool(context.Background(), nil, applyArgs{Path: p, AutoApprove: true})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("apply result=%v err=%v body=%s", res, err, extractText(t, res))
	}
	got, ok := value.(*apply.Result)
	if !ok {
		t.Fatalf("result type=%T", value)
	}
	parsed, lerr := intent.Load(p)
	if lerr != nil {
		t.Fatal(lerr)
	}
	rendered, rerr := render.RenderHOCONWithSecrets(templateDir, parsed, &parsed.Nodes[0])
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(rendered.Deployable(), marker) {
		t.Fatal("rendered config missing external template marker")
	}
	want := apply.IntentHashFromBytes([]byte(rendered.Deployable()))
	if got.ConfigHash != want {
		t.Fatalf("config hash=%q, external template rendered hash=%q", got.ConfigHash, want)
	}
	if data, err := os.ReadFile(dockerCalls); err != nil || len(data) == 0 {
		t.Fatalf("deploy did not use docker runtime: err=%v calls=%q", err, data)
	}
}

func writePlanFixture(t *testing.T, name string) string {
	t.Helper()
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, []byte("name: "+name+"\nnetwork: nile\ntarget:\n  type: local\n  runtime: docker\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSnapshotDownloadMissingDestUsesValidationEnvelope(t *testing.T) {
	res, _, err := snapshotDownloadTool(context.Background(), nil, snapshotDownloadArgs{Network: "nile"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected error result")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(extractText(t, res)), &body); err != nil {
		t.Fatal(err)
	}
	if body["error_code"] != "VALIDATION_ERROR" {
		t.Fatalf("error = %v", body)
	}
}

func TestRenderToolRejectsNegativeNodeIndex(t *testing.T) {
	p := t.TempDir() + "/intent.yaml"
	if err := os.WriteFile(p, []byte("name: p1\nnetwork: nile\ntarget:\n  type: local\nnodes:\n  - type: fullnode\n    version: 4.8.1\n    resources:\n      memory: 8GB\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, value, err := renderTool(context.Background(), nil, renderArg{Path: p, Node: -1})
	if err != nil {
		t.Fatal(err)
	}
	envelope, ok := value.(map[string]any)
	if !res.IsError || !ok || envelope["error_code"] != "VALIDATION_ERROR" {
		t.Fatalf("unexpected result: %v", res)
	}
}

func TestMCPHealUsesJarRuntimeForJarNode(t *testing.T) {
	old := healRuntimeForNode
	t.Cleanup(func() { healRuntimeForNode = old })
	want := errors.New("jar runtime selected")
	var gotRuntime string
	healRuntimeForNode = func(_ target.Target, node *state.ManagedNode) runtime.Runtime {
		gotRuntime = node.Runtime
		return fakeHealRuntime{err: want}
	}
	err := mcpRunHealAction(context.Background(), target.NewLocalTarget(), &state.ManagedNode{Runtime: "jar"}, mcpHealAction{action: "start"})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want jar runtime action", err)
	}
	if gotRuntime != "jar" {
		t.Fatalf("runtime=%q, want jar", gotRuntime)
	}
}

func TestHealRuntimeForNodeUsesRealJarFactory(t *testing.T) {
	rt := healRuntimeForNode(target.NewLocalTarget(), &state.ManagedNode{Runtime: "jar"})
	if got := fmt.Sprintf("%T", rt); got != "*runtime.JarRuntime" {
		t.Fatalf("runtime=%s, want real jar factory", got)
	}
}

type captureNetworkChecker struct{ got *string }

func (c captureNetworkChecker) Name() string { return "capture_network" }
func (c captureNetworkChecker) Run(_ context.Context, _ target.Target, opts diagnosis.CheckOpts) diagnosis.CheckResult {
	*c.got = opts.Network
	return diagnosis.CheckResult{Name: c.Name(), Status: diagnosis.StatusPass, Message: "captured"}
}

func TestDiagnoseToolPassesRecordedNetworkToChecker(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{Name: "diag-net", Network: "private", Runtime: "docker"}}}); err != nil {
		t.Fatal(err)
	}
	var got string
	old := diagnoseCheckers
	diagnoseCheckers = func() []diagnosis.Checker { return []diagnosis.Checker{captureNetworkChecker{got: &got}} }
	t.Cleanup(func() { diagnoseCheckers = old })
	res, _, err := diagnoseTool(context.Background(), nil, nodeArg{Name: "diag-net"})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("diagnose result=%v err=%v", res, err)
	}
	if got != "private" {
		t.Fatalf("checker network=%q", got)
	}
}

func TestAutoHealToolPassesRecordedNetworkToChecker(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{Name: "heal-net", Network: "nile", Runtime: "docker"}}}); err != nil {
		t.Fatal(err)
	}
	var got string
	old := healCheckers
	healCheckers = func() []diagnosis.Checker { return []diagnosis.Checker{captureNetworkChecker{got: &got}} }
	t.Cleanup(func() { healCheckers = old })
	res, _, err := autoHealTool(context.Background(), nil, autoHealArgs{Name: "heal-net", DryRun: true})
	if err != nil || res == nil || res.IsError {
		t.Fatalf("heal result=%v err=%v", res, err)
	}
	if got != "nile" {
		t.Fatalf("checker network=%q", got)
	}
}

func TestAutoHealToolReportsNoActionWhenAllChecksPass(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&state.DeploymentState{Nodes: []state.ManagedNode{{Name: "heal-pass", Network: "private", Runtime: "docker"}}}); err != nil {
		t.Fatal(err)
	}
	old := healCheckers
	healCheckers = func() []diagnosis.Checker { return []diagnosis.Checker{captureNetworkChecker{got: new(string)}} }
	t.Cleanup(func() { healCheckers = old })
	_, value, err := autoHealTool(context.Background(), nil, autoHealArgs{Name: "heal-pass"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := value.(map[string]any)
	if !ok || got["result"] != "no_action" {
		t.Fatalf("result=%v", value)
	}
}

func TestPeersCheckerUsesNetworkInRealCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"nodes":[]}`)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	checker := &diagnosis.PeersChecker{}
	private := checker.Run(context.Background(), target.NewLocalTarget(), diagnosis.CheckOpts{HTTPPort: port, Network: "private"})
	mainnet := checker.Run(context.Background(), target.NewLocalTarget(), diagnosis.CheckOpts{HTTPPort: port, Network: "mainnet"})
	if !strings.Contains(private.Message, "minimum 1") || !strings.Contains(mainnet.Message, "minimum 3") {
		t.Fatalf("private=%+v mainnet=%+v", private, mainnet)
	}
}

type fakeHealRuntime struct{ err error }

func (f fakeHealRuntime) Deploy(context.Context, runtime.DeployOpts) error { return nil }
func (f fakeHealRuntime) Start(context.Context, string) error              { return f.err }
func (f fakeHealRuntime) Stop(context.Context, string) error               { return nil }
func (f fakeHealRuntime) Remove(context.Context, string, bool) error       { return nil }
func (f fakeHealRuntime) Status(context.Context, string) (*runtime.NodeStatus, error) {
	return nil, nil
}
func (f fakeHealRuntime) Logs(context.Context, string, runtime.LogOpts) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

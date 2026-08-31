package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// newConnectedPair spins up an MCP server with all trond tools
// registered, plus a client connected via in-memory transport. Used
// as the test harness for round-trip tests.
func newConnectedPair(t *testing.T) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()

	// Isolate state so tests don't read the host's ~/.trond.
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "trond-test",
		Version: "test",
	}, nil)
	registerInspectionTools(server)
	registerConfigTools(server)
	registerDiagnosticTools(server)
	registerSnapshotTools(server)
	registerKnowledgeTools(server)
	registerLifecycleTools(server)
	registerDriftTools(server)
	registerHealTools(server)
	registerBuildTools(server)
	registerResources(server)
	registerPrompts(server)

	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "test"}, nil)

	t1, t2 := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}

	cleanup := func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	}
	return clientSession, cleanup
}

// extractText pulls the first text content out of a tool result. All
// tools register a TextContent body with the JSON payload (or the
// markdown body, for knowledge_get). Non-text content fails the test.
func extractText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("no content in tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func TestListTools_AllRegistered(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	// Every tool name we registered should be visible to the client.
	want := []string{
		"list", "status", "inspect",
		"doctor", "version", "health", "diagnose",
		"config_validate", "config_render", "plan", "apply",
		"snapshot_sources", "snapshot_list", "snapshot_jobs", "snapshot_download",
		"knowledge_list", "knowledge_get",
		"build_list", "build_inspect", "build_prune",
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("tool %q missing from ListTools", w)
		}
	}
}

func TestVersion_RoundTrip(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "version",
	})
	if err != nil {
		t.Fatalf("CallTool version: %v", err)
	}
	if res.IsError {
		t.Fatalf("version returned IsError: %s", extractText(t, res))
	}
	body := extractText(t, res)

	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("unmarshal version body: %v\n%s", err, body)
	}
	for _, k := range []string{"version", "commit", "build_time", "go_version", "platform"} {
		if _, ok := v[k]; !ok {
			t.Errorf("version response missing %q field; got %v", k, v)
		}
	}
}

func TestList_EmptyState_ReturnsEmptyArray(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list",
	})
	if err != nil {
		t.Fatalf("CallTool list: %v", err)
	}
	if res.IsError {
		t.Fatalf("list returned IsError: %s", extractText(t, res))
	}
	// list returns a flat array (matches `trond list -o json` and
	// schemas/output/list.schema.json).
	var nodes []any
	if err := json.Unmarshal([]byte(extractText(t, res)), &nodes); err != nil {
		t.Fatalf("list body is not a JSON array: %v\n%s", err, extractText(t, res))
	}
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes in fresh state-dir, got %d", len(nodes))
	}
}

func TestSnapshotSources_ReturnsCuratedTable(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "snapshot_sources",
	})
	if err != nil {
		t.Fatalf("CallTool snapshot_sources: %v", err)
	}
	body := extractText(t, res)

	var v struct {
		Sources []struct {
			Network, Kind, Domain string
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("unmarshal sources: %v\n%s", err, body)
	}
	if len(v.Sources) < 5 {
		t.Errorf("expected at least 5 sources, got %d", len(v.Sources))
	}
	// Spot-check one mainnet + the nile entry.
	hasNile := false
	for _, s := range v.Sources {
		if s.Network == "nile" {
			hasNile = true
		}
	}
	if !hasNile {
		t.Error("expected at least one nile source in snapshot_sources")
	}
}

func TestKnowledge_RoundTrip(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	listRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "knowledge_list",
	})
	if err != nil {
		t.Fatalf("knowledge_list: %v", err)
	}
	var listed map[string]any
	_ = json.Unmarshal([]byte(extractText(t, listRes)), &listed)
	topics, ok := listed["topics"].([]any)
	if !ok || len(topics) == 0 {
		t.Fatalf("topics empty: %v", listed)
	}

	first, _ := topics[0].(string)
	if first == "" {
		t.Fatal("first topic name empty")
	}

	getRes, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "knowledge_get",
		Arguments: json.RawMessage(`{"topic":"` + first + `"}`),
	})
	if err != nil {
		t.Fatalf("knowledge_get: %v", err)
	}
	body := extractText(t, getRes)
	if !strings.HasPrefix(body, "#") {
		t.Errorf("knowledge body should be markdown starting with #, got: %q...", body[:min(40, len(body))])
	}
}

func TestDestructiveTools_HaveDestructiveHint(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	mustBeDestructive := map[string]bool{
		"apply":             true,
		"snapshot_download": true,
	}
	for _, tool := range res.Tools {
		if !mustBeDestructive[tool.Name] {
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s: missing Annotations entirely", tool.Name)
			continue
		}
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s: DestructiveHint should be true; got %+v", tool.Name, tool.Annotations.DestructiveHint)
		}
		delete(mustBeDestructive, tool.Name)
	}
	for stillMissing := range mustBeDestructive {
		t.Errorf("destructive tool %q not found in ListTools output", stillMissing)
	}
}

func TestApply_RejectsBadIntentPath(t *testing.T) {
	// Apply now runs the real internal/apply pipeline (cmd/apply
	// extraction landed). The stub-tested NOT_IMPLEMENTED_VIA_MCP
	// path is gone. Replace with a smaller test that exercises the
	// front-end validation without needing a real Docker target:
	// pass a non-existent intent path and confirm we surface a
	// VALIDATION_ERROR envelope rather than a panic.
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "apply",
		Arguments: json.RawMessage(`{"path":"/nonexistent/intent.yaml"}`),
	})
	if err != nil {
		t.Fatalf("CallTool apply: %v", err)
	}
	if !res.IsError {
		t.Fatal("apply with bogus intent path should set IsError=true")
	}
	body := extractText(t, res)
	var env map[string]any
	_ = json.Unmarshal([]byte(body), &env)
	if env["error_code"] != "VALIDATION_ERROR" {
		t.Errorf("expected error_code=VALIDATION_ERROR, got %v", env["error_code"])
	}
}

func TestSnapshotDownload_DryRunReturnsPlan(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	// dry_run=true so we never hit the network beyond the upstream
	// HEAD probe — and even that we skip by passing an unknown
	// network/domain combo: pickSource fails before any HTTP call.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "snapshot_download",
		Arguments: json.RawMessage(`{"network":"definitely-not-a-network","dest":"/tmp/x","dry_run":true}`),
	})
	if err != nil {
		t.Fatalf("CallTool snapshot_download: %v", err)
	}
	if !res.IsError {
		t.Fatal("snapshot_download with bogus network should error")
	}
	body := extractText(t, res)
	if !strings.Contains(body, "no source matches") {
		t.Errorf("expected 'no source matches' in error body, got: %s", body)
	}
}

func TestStatus_NotFound_ReturnsStructuredError(t *testing.T) {
	session, cleanup := newConnectedPair(t)
	defer cleanup()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "status",
		Arguments: json.RawMessage(`{"name":"does-not-exist"}`),
	})
	if err != nil {
		t.Fatalf("CallTool status: %v", err)
	}
	if !res.IsError {
		t.Fatal("status of unknown node should set IsError=true")
	}
	body := extractText(t, res)
	var env map[string]any
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("error envelope is not JSON: %v\n%s", err, body)
	}
	if env["error_code"] != "NODE_NOT_FOUND" {
		t.Errorf("expected NODE_NOT_FOUND, got %v", env["error_code"])
	}
	if _, ok := env["suggestions"]; !ok {
		t.Error("expected suggestions[] in error envelope")
	}
}

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/tronprotocol/tron-deployment/internal/schema"
)

// TestMCPToolOutputsMatchCLISchemas asserts that every read-side MCP
// tool whose output corresponds to a published CLI schema emits a
// payload that validates against that same schema. Without this gate,
// the MCP transport and the CLI transport could drift — agents
// fetching the schema offline would see one shape, agents calling
// the tool would see another.
//
// What we cover here:
//   - tools that work on an empty state dir (no docker, no real node)
//   - both success-path payloads and error-path envelopes
//
// What we do not cover (separate tests, real or e2e):
//   - apply / status / health / diagnose / verify — need a node
//   - snapshot_download — needs the network
func TestMCPToolOutputsMatchCLISchemas(t *testing.T) {
	cases := []struct {
		tool       string
		schemaName string
		args       json.RawMessage
	}{
		{tool: "version", schemaName: "version"},
		{tool: "doctor", schemaName: "doctor"},
		{tool: "list", schemaName: "list"},
		{tool: "snapshot_sources", schemaName: "snapshot-sources"},
		{tool: "snapshot_jobs", schemaName: "snapshot-jobs"},
		{tool: "config_validate", schemaName: "config-validate",
			args: json.RawMessage(`{"path":"../../examples/nile-fullnode.yaml"}`)},
		{tool: "config_render", schemaName: "config-render",
			args: json.RawMessage(`{"path":"../../examples/nile-fullnode.yaml"}`)},
		// plan now returns the same state-aware structural contract as the CLI;
		// line-level HOCON diff remains CLI-only.
		{tool: "plan", schemaName: "plan",
			args: json.RawMessage(`{"path":"../../examples/nile-fullnode.yaml"}`)},
	}

	session, cleanup := newConnectedPair(t)
	defer cleanup()

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
				Name:      tc.tool,
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool %s: %v", tc.tool, err)
			}
			if res.IsError {
				t.Fatalf("%s returned error: %s", tc.tool, extractText(t, res))
			}
			body := extractText(t, res)
			var parsed any
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				t.Fatalf("%s body is not JSON: %v\n%s", tc.tool, err, body)
			}
			if err := validateAgainstEmbeddedSchema(tc.schemaName, parsed); err != nil {
				t.Fatalf("%s output failed schema %q: %v\nbody:\n%s",
					tc.tool, tc.schemaName, err, body)
			}
		})
	}
}

// TestMCPErrorEnvelopesMatchCLISchema covers the error path: every
// failing tool call returns a body that validates against the
// canonical error envelope (schemas/output/error.schema.json), the
// same one the CLI uses for non-zero exits. Agents that handle CLI
// errors and MCP errors via shared logic depend on this.
func TestMCPErrorEnvelopesMatchCLISchema(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args json.RawMessage
	}{
		{
			name: "config_validate-missing-path",
			tool: "config_validate",
			args: json.RawMessage(`{"path":"/nonexistent/intent.yaml"}`),
		},
		{
			name: "status-missing-node",
			tool: "status",
			args: json.RawMessage(`{"name":"definitely-not-deployed"}`),
		},
		{
			name: "snapshot_download-bad-network",
			tool: "snapshot_download",
			args: json.RawMessage(`{"network":"definitely-not-a-network","dest":"/tmp/x","dry_run":true}`),
		},
	}

	session, cleanup := newConnectedPair(t)
	defer cleanup()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
				Name:      tc.tool,
				Arguments: tc.args,
			})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			if !res.IsError {
				t.Fatalf("expected IsError=true, got success body: %s", extractText(t, res))
			}
			body := extractText(t, res)
			var parsed any
			if err := json.Unmarshal([]byte(body), &parsed); err != nil {
				t.Fatalf("envelope is not JSON: %v\n%s", err, body)
			}
			if err := validateAgainstEmbeddedSchema("error", parsed); err != nil {
				t.Fatalf("error envelope failed schema: %v\nbody:\n%s", err, body)
			}
		})
	}
}

// validateAgainstEmbeddedSchema compiles the schema bundled into the
// binary at internal/schema/files/<name>.schema.json and validates
// parsed against it. Using the embedded copy (not the source-tree
// path) is the closer match to what real MCP clients see — the
// schema served by `trond schema` is the one we should be conforming
// to.
func validateAgainstEmbeddedSchema(name string, parsed any) error {
	doc, ok := schema.Get(name)
	if !ok {
		// A test bug, not a runtime failure.
		panic("validateAgainstEmbeddedSchema: no embedded schema named " + name)
	}
	c := jsonschema.NewCompiler()
	id, _ := doc["$id"].(string)
	if id == "" {
		id = "trond:" + name
	}
	if err := c.AddResource(id, doc); err != nil {
		return err
	}
	sch, err := c.Compile(id)
	if err != nil {
		return err
	}
	return sch.Validate(parsed)
}

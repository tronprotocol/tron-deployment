// Package mcp wires trond's commands as Model Context Protocol tools so
// chat-based / IDE-embedded agents (Claude Desktop, Cursor, Cline,
// Continue.dev, Zed AI, ChatGPT Apps, etc.) can call them as
// structured tool functions instead of shelling out.
//
// Design choices:
//
//   - Tools call directly into trond's internal/* packages (intent,
//     render, snapshot, state, ...). They do NOT fork+exec the trond
//     binary. The whole MCP handler runs in-process so tool calls are
//     cheap and connections (SSH, docker) can be reused across calls.
//
//   - Tool input/output schemas are derived from typed Go structs via
//     the SDK's struct-tag inspection (`json`, `jsonschema`). This
//     keeps the schema and the tool implementation in lockstep — when
//     a field is added to the struct, the published schema follows.
//
//   - Destructive operations (apply, snapshot download with --force,
//     etc.) carry the MCP `destructiveHint` annotation so MCP-aware
//     clients can prompt the user before invoking them.
//
//   - Long-running operations (snapshot download) emit MCP progress
//     notifications (req.Session.NotifyProgress). Clients like Claude
//     Desktop render these as live progress bars.
//
//   - Errors are returned via the MCP CallToolResult.IsError field,
//     plus a Content slice with the error envelope JSON (matching the
//     same shape as `trond <cmd> -o json` returns on failure). This
//     lets the LLM see the error_code and suggestions[] structure
//     it would see from the CLI.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// Run starts a trond MCP server speaking JSON-RPC over the given
// reader/writer pair (stdio for the cobra subcommand; net.Conn for the
// optional --listen mode). It blocks until the client disconnects.
//
// trondVersion is stamped into the server's Implementation block so
// MCP clients know which trond they're talking to. Empty string is
// allowed — the server still works.
func Run(ctx context.Context, in io.Reader, out io.Writer, trondVersion string) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "trond",
		Title:   "TRON node deployment",
		Version: trondVersion,
	}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})

	// Register every tool with the server. Each tools_*.go file owns
	// a logical group of tools and wires them via mcp.AddTool.
	registerLifecycleTools(server)
	registerInspectionTools(server)
	registerConfigTools(server)
	registerDiagnosticTools(server)
	registerSnapshotTools(server)
	registerKnowledgeTools(server)
	registerDriftTools(server)
	registerHealTools(server)
	registerBuildTools(server)
	registerShadowforkTools(server)

	// Beyond tools, MCP exposes resources (read-only data the agent
	// can attach to a conversation) and prompts (pre-baked workflow
	// templates the user invokes via slash-commands). Resources let
	// the agent see state.json + audit.log + the schema manifest
	// without per-call tool invocations; prompts encode AGENTS.md's
	// canonical workflows so a fresh agent session lands on the
	// right tool sequence the first time.
	registerResources(server)
	registerPrompts(server)

	// MCP's stdio transport is a thin wrapper around io.Reader/Writer
	// pairs; tests pass net.Pipe() ends, real use passes os.Stdin/Stdout.
	return server.Run(ctx, &mcp.StdioTransport{})
}

// serverInstructions is exposed to clients during initialization so an
// agent's "system prompt" automatically picks up the operational
// guidance from AGENTS.md without the user having to paste it.
const serverInstructions = `You are connected to trond — a CLI for deploying and managing TRON
blockchain nodes (mainnet / Nile testnet / private networks). Available
tools:

  Read-only:    doctor, version, list, status, inspect, health,
                knowledge_list, knowledge_get, snapshot_sources,
                snapshot_list, snapshot_jobs
  Validation:   config_validate, config_render, verify_config, plan,
                diagnose
  Destructive:  apply, snapshot_download, auto_heal

  Build cache (list/inspect read-only; prune deletes):
                build_list, build_inspect, build_prune
  Shadow-fork (mutates a halted node data dir in place):
                shadow_fork_mutate

For deploy / diagnose / snapshot / private-network workflows, follow
the canonical chains documented in AGENTS.md (the TRON deployment
repo's agent guide). Always validate intent files with config_validate
before plan/apply. Pass auto_approve=true to apply only when the user
has explicitly approved the diff shown by plan.

Beyond tools, this server exposes:

  Resources (read-only data — attach to context, don't tool-call):
    trond://state                     — current state.json
    trond://audit-log                 — last 200 audit log entries
    trond://schema-manifest           — all output schemas + version
    trond://nodes/{name}/endpoints    — one node's endpoints + ports
    trond://nodes/{name}/conf         — one node's live HOCON conf

  Prompts (slash-command workflows the user picks):
    deploy_fullnode         — config_validate → plan → apply → status
    diagnose_failing_node   — status → diagnose → health
    setup_private_network   — multi-node bootstrap with SR_PRIVATE_KEY
    recover_failed_upgrade  — diagnose → rollback → verify

Long-running tools emit MCP progress notifications; surface those to
the user.`

// errResult builds a CallToolResult representing a failed tool call.
// The error envelope JSON is in Content so the LLM can see the
// error_code and suggestions[] just like it would from the CLI.
func errResult(err error) (*mcp.CallToolResult, any, error) {
	envelope := envelopeFromError(err)
	body, marshalErr := json.MarshalIndent(envelope, "", "  ")
	if marshalErr != nil {
		body = fmt.Appendf(nil, "trond error (envelope marshal failed: %v)\n%v", marshalErr, err)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, envelope, nil
}

// envelopeFromError mirrors the shape in schemas/output/error.schema.json.
func envelopeFromError(err error) map[string]any {
	if se, ok := err.(*output.StructuredError); ok {
		out := map[string]any{
			"error_code": se.Code,
			"exit_code":  se.ExitCode,
			"message":    se.Message,
		}
		if len(se.Suggestions) > 0 {
			out["suggestions"] = se.Suggestions
		}
		return out
	}
	return map[string]any{
		"error_code": "INTERNAL_ERROR",
		"exit_code":  1,
		"message":    err.Error(),
	}
}

// jsonResult marshals a value into a CallToolResult with a single
// JSON content block.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(fmt.Errorf("marshal tool result: %w", err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, v, nil
}

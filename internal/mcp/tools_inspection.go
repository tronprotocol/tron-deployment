package mcp

import (
	"context"
	"maps"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// registerInspectionTools wires the read-only "what's deployed?"
// tools. None of these mutate state; they're safe to call freely
// from an LLM without prompting the user.

type emptyArgs struct{}

// addBuildIdentity adds build_cache_key + the resolved build_revision (B1)
// to a row when the node was built from source. No-op for pre-built
// image/jar nodes. Shared by list/status/inspect so the shape is identical.
func addBuildIdentity(row map[string]any, n *state.ManagedNode) {
	if n.BuildCacheKey == "" {
		return
	}
	row["build_cache_key"] = n.BuildCacheKey
	if rev := n.BuildRevision(); rev != "" {
		row["build_revision"] = rev
	}
}

type nodeArg struct {
	Name string `json:"name" jsonschema:"name of the managed node (must match intent.name)"`
}

func registerInspectionTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list",
		Title:       "List managed nodes",
		Description: "Returns every node trond is currently managing, with status, runtime, version, and labels. Equivalent to `trond list -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, listNodes)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "status",
		Title:       "Node status",
		Description: "Detailed status for one node. Combines stored state with a best-effort live HTTP probe (block height, peer count, sync state, endpoints). Equivalent to `trond status <name> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, statusForNode)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "inspect",
		Title:       "Inspect endpoints",
		Description: "Manifest of every node's endpoints, container IPs, and labels. Used by test harnesses to discover where to send traffic. Equivalent to `trond inspect -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, inspectAllNodes)
}

func listNodes(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	_, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}
	// Reshape into the same JSON we'd emit from `trond list -o json`.
	// Schema: schemas/output/list.schema.json — a flat array, not a
	// {nodes: [...]} object. Matching the CLI shape lets MCP-aware
	// agents pass results into pipelines that already parse `trond
	// list` output.
	rows := make([]map[string]any, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		row := map[string]any{
			"name":         n.Name,
			"status":       n.Status,
			"runtime":      n.Runtime,
			"version":      n.Version,
			"last_applied": n.LastApplied,
			"target_type":  n.Target.Type,
			"network":      n.Network,
			"is_private":   intent.IsPrivate(n.Network),
		}
		addBuildIdentity(row, &n)
		if len(n.Labels) > 0 {
			row["labels"] = n.Labels
		}
		rows = append(rows, row)
	}
	return jsonResult(rows)
}

func statusForNode(ctx context.Context, _ *mcp.CallToolRequest, args nodeArg) (*mcp.CallToolResult, any, error) {
	store, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}
	node := store.GetNode(st, args.Name)
	if node == nil {
		return errResult(notFound("status", args.Name))
	}
	out := map[string]any{
		"name":         node.Name,
		"status":       node.Status,
		"runtime":      node.Runtime,
		"version":      node.Version,
		"target":       node.Target,
		"last_applied": node.LastApplied,
		"intent_hash":  node.IntentHash,
		"config_hash":  node.ConfigHash,
		"labels":       node.Labels,
		"network":      node.Network,
		"is_private":   intent.IsPrivate(node.Network),
		// A1 parity with `trond status -o json`: healthy seeded false
		// (live probe flips it true), logs is the runtime-discriminated
		// log locator for an external consumer.
		"healthy": false,
		"logs":    apply.LogsDescriptor(node),
	}
	addBuildIdentity(out, node)
	// Live probe + container_id — best effort, resolving the target at most
	// once. We must not open an SSH connection just to read container_id
	// from a stopped remote node (mcpResolveTargetFromNode.Connect() isn't
	// bound by the probe context), so we only resolve when the node is
	// running (the probe needs a target anyway) or it's a LOCAL docker node
	// (cheap). An ssh docker node still gets container_id, but only
	// piggybacked on the running-node resolution. Errors are dropped.
	needLocalDockerID := node.Runtime == "docker" && node.Target.Type == "local"
	if node.Status == "running" || needLocalDockerID {
		if tgt, err := target.FromManagedNode(node); err == nil {
			if c, ok := any(tgt).(interface{ Close() error }); ok {
				defer c.Close()
			}
			// Probe first with its own budget so a slow container_id lookup
			// can't starve the primary `healthy` signal.
			if node.Status == "running" {
				pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
				maps.Copy(out, apply.LiveStatus(pctx, tgt, node))
				pcancel()
			}
			cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
			if id := apply.ContainerID(cctx, tgt, node); id != "" {
				out["container_id"] = id
			}
			ccancel()
		}
	}
	return jsonResult(out)
}

func inspectAllNodes(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	_, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}
	rows := make([]map[string]any, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		row := map[string]any{
			"name":       n.Name,
			"status":     n.Status,
			"runtime":    n.Runtime,
			"network":    n.Network,
			"is_private": intent.IsPrivate(n.Network),
			// logs is static (no docker call), so it's safe to include in
			// the deliberately-static MCP inspect. container_id is omitted
			// here (it needs a live docker query) — callers wanting it use
			// the `status` tool, which already resolves a target.
			"logs": apply.LogsDescriptor(&n),
		}
		addBuildIdentity(row, &n)
		// Endpoints: we have HTTPPort/GRPCPort persisted in state from
		// apply; cmd/inspect.go's enrichment with container_ip
		// requires a live docker query, skipped for the static MCP
		// version. Agents that need live container IPs should call
		// `trond inspect` via shell or wait for the runtime probe
		// extraction.
		eps := map[string]string{}
		if n.HTTPPort != 0 {
			eps["http"] = apply.HTTPURL(target.EndpointHost(n.Target.Type, n.Target.Host), n.HTTPPort)
		}
		if n.GRPCPort != 0 {
			eps["grpc"] = apply.GRPCAddr(target.EndpointHost(n.Target.Type, n.Target.Host), n.GRPCPort)
		}
		if len(eps) > 0 {
			row["endpoints"] = eps
		}
		if len(n.Labels) > 0 {
			row["labels"] = n.Labels
		}
		rows = append(rows, row)
	}
	return jsonResult(map[string]any{"nodes": rows})
}

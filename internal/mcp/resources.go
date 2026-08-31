package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// registerResources wires read-only data sources as MCP resources.
// Resources are conceptually different from tools: tools are
// "actions" (verb), resources are "data" (noun). MCP-aware clients
// like Claude Desktop surface resources in a file-system-like UI
// the user can attach to a conversation.
//
// Static resources (one fixed URI):
//
//   - trond://state             — full state.json (every managed node)
//   - trond://audit-log         — last 200 audit log entries
//   - trond://schema-manifest   — every embedded output schema + version
//
// Dynamic resource templates (URI variables):
//
//   - trond://nodes/{name}/endpoints  — one node's endpoints + ports
//   - trond://nodes/{name}/conf       — one node's live HOCON conf
//
// Templates let agents attach a single node's data without dragging
// in unrelated nodes — important when state has 10s of managed
// nodes and the agent is only reasoning about one.
func registerResources(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         "trond://state",
		Name:        "state.json",
		Description: "The state.json file at $TROND_STATE_DIR (or ~/.trond). One row per managed node — name, version, target, runtime, ports.",
		MIMEType:    "application/json",
	}, readStateResource)

	s.AddResource(&mcp.Resource{
		URI:         "trond://audit-log",
		Name:        "audit.log (recent)",
		Description: "Up to the last 200 entries of audit.log (JSONL). Use to answer 'what did we do to this node yesterday' without invoking the events tool.",
		MIMEType:    "application/x-ndjson",
	}, readAuditLogResource)

	s.AddResource(&mcp.Resource{
		URI:         "trond://schema-manifest",
		Name:        "schema manifest",
		Description: "SchemaVersion + every embedded output schema. Authoritative reference for the trond CLI surface.",
		MIMEType:    "application/json",
	}, readSchemaManifestResource)

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "trond://nodes/{name}/endpoints",
		Name:        "node endpoints",
		Description: "Endpoints (http / grpc / labels / target) for a single managed node. Smaller than trond://state when an agent only reasons about one node.",
		MIMEType:    "application/json",
	}, readNodeEndpointsResource)

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "trond://nodes/{name}/conf",
		Name:        "node live HOCON conf",
		Description: "The .conf file currently in use by the node's running container (docker exec cat). Read-only.",
		MIMEType:    "text/plain",
	}, readNodeConfResource)
}

func readStateResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}, nil
}

const auditLogTailMax = 200

func readAuditLogResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	body, err := os.ReadFile(paths.AuditLog())
	if err != nil {
		if os.IsNotExist(err) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{{
					URI:      req.Params.URI,
					MIMEType: "application/x-ndjson",
					Text:     "",
				}},
			}, nil
		}
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/x-ndjson",
			Text:     tailLines(string(body), auditLogTailMax),
		}},
	}, nil
}

func readSchemaManifestResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	body, err := schemaManifestJSON()
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     body,
		}},
	}, nil
}

func readNodeEndpointsResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name, err := nodeNameFromURI(req.Params.URI, "/endpoints")
	if err != nil {
		return nil, err
	}
	node, err := loadNodeByName(name)
	if err != nil {
		return nil, err
	}
	host := target.EndpointHost(node.Target.Type, node.Target.Host)
	endpoints := map[string]any{
		"name":    node.Name,
		"runtime": node.Runtime,
		"target":  node.Target,
		"endpoints": map[string]string{
			"http": apply.HTTPURL(host, node.HTTPPort),
			"grpc": apply.GRPCAddr(host, node.GRPCPort),
		},
		"version": node.Version,
		"labels":  node.Labels,
	}
	body, err := json.MarshalIndent(endpoints, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "application/json",
			Text:     string(body),
		}},
	}, nil
}

func readNodeConfResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name, err := nodeNameFromURI(req.Params.URI, "/conf")
	if err != nil {
		return nil, err
	}
	node, err := loadNodeByName(name)
	if err != nil {
		return nil, err
	}
	tgt, err := target.FromManagedNode(node)
	if err != nil {
		return nil, err
	}
	if c, ok := any(tgt).(interface{ Close() error }); ok {
		defer c.Close()
	}
	live, err := readLiveConfigForMCP(ctx, tgt, node)
	if err != nil {
		return nil, err
	}
	// This resource hands the whole conf to the MCP client, and from
	// there to a model provider — it is the one surface whose full text
	// leaves the machine by design. A witness node's conf carries its
	// signing key, so redact before it goes out. The drift tool reads
	// the same helper and keeps the raw text, because it compares
	// against a rendered config and a redacted side would report every
	// witness node as drifted.
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "text/plain",
			Text:     strings.Join(render.RedactWitnessLines(strings.Split(live, "\n")), "\n"),
		}},
	}, nil
}

// nodeNameFromURI extracts <name> from URIs of the form
// trond://nodes/<name><suffix>. Returns a clean error when the URI
// doesn't fit the expected shape — agents see it as a normal MCP
// error rather than a panic.
func nodeNameFromURI(uri, suffix string) (string, error) {
	const prefix = "trond://nodes/"
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) {
		return "", fmt.Errorf("URI %q does not match trond://nodes/<name>%s", uri, suffix)
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), suffix)
	if mid == "" || strings.ContainsAny(mid, "/?#") {
		return "", fmt.Errorf("URI %q has empty or malformed <name> segment", uri)
	}
	return mid, nil
}

func loadNodeByName(name string) (*state.ManagedNode, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	st, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	node := store.GetNode(st, name)
	if node == nil {
		return nil, fmt.Errorf("no managed node named %q", name)
	}
	return node, nil
}

// tailLines returns at most n lines from the end of s, newline-
// preserved. Bounded so a long-running operator's audit.log doesn't
// blow out the agent's context window.
func tailLines(s string, n int) string {
	if s == "" {
		return ""
	}
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count > n {
				return s[i+1:]
			}
		}
	}
	return s
}

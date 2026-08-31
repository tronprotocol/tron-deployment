package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
)

// registerConfigTools wires the read-only config-plane tools:
// validate, render, plan. These don't mutate any state — agents can
// freely use them to inspect what `apply` would do.

type intentPathArg struct {
	Path string `json:"path" jsonschema:"absolute path to an intent.yaml file"`
}

type renderArg struct {
	Path string `json:"path" jsonschema:"absolute path to an intent.yaml file"`
	Node int    `json:"node,omitempty" jsonschema:"render only the node at this index (omit to render all nodes)"`
}

func registerConfigTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "config_validate",
		Title:       "Validate intent file",
		Description: "Validates an intent.yaml file's shape and field constraints. Always run this before plan/apply. Equivalent to `trond config validate <path> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, validateTool)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "config_render",
		Title:       "Render intent to HOCON + compose/systemd",
		Description: "Render the intent.yaml into the final java-tron HOCON config plus the compose / systemd file that would be written. Useful for previewing what apply would produce. A witness node's private key is never returned — it is replaced by a `<REDACTED:ENV_NAME>` placeholder and `redacted: true` is set, so the HOCON is a preview java-tron would reject; deploy with the apply tool instead. Equivalent to `trond config render <path> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, renderTool)
}

func validateTool(ctx context.Context, _ *mcp.CallToolRequest, args intentPathArg) (*mcp.CallToolResult, any, error) {
	parsed, err := intent.Load(args.Path)
	if err != nil {
		// Wrap with VALIDATION_ERROR + suggestions so an agent has a
		// clear next step rather than the bare loader error.
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error()).
			WithSuggestions(
				"Confirm the path is absolute and the intent.yaml exists",
				"See examples/ in the trond repo for valid intent.yaml templates",
			))
	}
	return jsonResult(map[string]any{
		"valid":      true,
		"name":       parsed.Name,
		"network":    parsed.Network,
		"node_count": len(parsed.Nodes),
	})
}

func renderTool(ctx context.Context, _ *mcp.CallToolRequest, args renderArg) (*mcp.CallToolResult, any, error) {
	parsed, err := intent.Load(args.Path)
	if err != nil {
		return errResult(err)
	}
	if args.Node < 0 || args.Node > len(parsed.Nodes) {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("node index %d out of range; use 0 for all or 1-%d", args.Node, len(parsed.Nodes))))
	}

	// templateDir resolution: empty → embedded templates. We don't
	// expose a `template_dir` arg because MCP clients usually live on
	// the user's laptop where the embedded templates are correct.
	templateDir := ""

	rendered := make([]map[string]any, 0, len(parsed.Nodes))
	anyRedacted := false
	for i := range parsed.Nodes {
		// args.Node uses 0 for "all" (default); 1-based index for filter.
		// So args.Node=2 → render only the second node (i=1).
		if args.Node != 0 && args.Node-1 != i {
			continue
		}
		node := &parsed.Nodes[i]
		// Preview surface returned to the MCP client (and therefore to
		// a third-party model provider): take the redacted display
		// form, never the deployable one.
		r, err := render.RenderHOCONWithSecrets(templateDir, parsed, node)
		if err != nil {
			return errResult(err)
		}
		memGB, err := render.ParseMemoryGB(node.Resources.Memory)
		if err != nil {
			return errResult(fmt.Errorf("invalid resources.memory %q: %w", node.Resources.Memory, err))
		}
		jvmArgs := render.JVMArgsString(memGB, 17, node.JVM)

		row := map[string]any{
			"index":    i,
			"name":     parsed.Name,
			"type":     node.Type,
			"hocon":    r.Config,
			"jvm_args": jvmArgs,
		}
		if r.Redacted {
			// Flags the HOCON as a preview: the witness key was
			// replaced by a placeholder java-tron will reject.
			row["redacted"] = true
			anyRedacted = true
		}
		runtime := parsed.Target.Runtime
		if runtime == "" {
			runtime = "docker"
		}
		switch runtime {
		case "docker":
			row["compose"] = render.RenderCompose(parsed.Name, parsed, node, "", jvmArgs, "")
		case "jar":
			row["systemd"] = render.RenderSystemdUnit(parsed, node, jvmArgs, "", "")
		}
		rendered = append(rendered, row)
	}
	payload := map[string]any{
		"name":    parsed.Name,
		"network": parsed.Network,
		"nodes":   rendered,
	}
	if anyRedacted {
		payload["redacted"] = true
	}
	return jsonResult(payload)
}

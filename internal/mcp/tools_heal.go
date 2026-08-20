package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// registerHealTools wires the auto_heal MCP tool — the in-process
// twin of `trond auto-heal`. Marked destructive because some
// remediation actions (start/restart) do change the runtime; agents
// must surface to the user before invoking unless dry_run=true.

type autoHealArgs struct {
	Name   string `json:"name" jsonschema:"managed node name"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"if true, propose actions without executing"`
}

func registerHealTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "auto_heal",
		Title:       "Auto-fix known fail checks on a node",
		Description: "Run trond's diagnostic suite, then apply documented remediations to fail checks (e.g. port_listening fail + status=stopped → start). Only safe, idempotent actions are mapped; everything else surfaces in skipped[] with suggestions for the human. Equivalent to `trond auto-heal <node> -o json`. Pass dry_run=true to propose without acting.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrTrue(),
			IdempotentHint:  true,
		},
	}, autoHealTool)
}

func autoHealTool(ctx context.Context, _ *mcp.CallToolRequest, args autoHealArgs) (*mcp.CallToolResult, any, error) {
	if args.Name == "" {
		return errResult(fmt.Errorf("name is required"))
	}

	// Hold the state lock across the load-modify-save cycle, the same way
	// the lifecycle tool does — heal writes the node list back after the
	// repair runs.
	lock := state.NewLock(paths.BaseDir())
	if err := lock.Acquire(); err != nil {
		return errResult(fmt.Errorf("acquire state lock: %w", err))
	}
	defer lock.Release()

	store, err := state.NewStore(paths.State())
	if err != nil {
		return errResult(err)
	}
	st, err := store.Load()
	if err != nil {
		return errResult(err)
	}
	node := store.GetNode(st, args.Name)
	if node == nil {
		return errResult(notFound("auto_heal", args.Name))
	}

	// --require-private / TROND_REQUIRE_PRIVATE (the C1 gate): the acting path
	// below starts the node and rewrites state, so it refuses a non-private
	// node just like the CLI's `trond auto-heal`. Enforced on the node's
	// RECORDED network (never a caller-supplied one) and BEFORE target
	// resolution, so an unreachable mainnet node still gets
	// PRIVATE_NETWORK_REQUIRED.
	//
	// Scoped to the acting path: with dry_run=true the loop `continue`s before
	// mcpRunHealAction and the store.Save, so the proposal-only preview stays
	// available under the gate.
	if !args.DryRun {
		if err := guard.Enforce(node.Network); err != nil {
			return errResult(err)
		}
	}

	tgt, err := mcpResolveTargetFromNode(node)
	if err != nil {
		return errResult(err)
	}
	if c, ok := any(tgt).(interface{ Close() error }); ok {
		defer c.Close()
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	opts := diagnosis.CheckOpts{
		NodeName:    node.Name,
		Runtime:     node.Runtime,
		HTTPPort:    node.HTTPPort,
		GRPCPort:    node.GRPCPort,
		InstallPath: node.InstallPath,
	}

	var (
		healed       []map[string]any
		skipped      []map[string]any
		stillFailing []diagnosis.CheckResult
	)
	for _, c := range diagnosis.AllCheckers() {
		r := c.Run(probeCtx, tgt, opts)
		if r.Status != diagnosis.StatusFail {
			continue
		}
		action, ok := mcpProposeHealAction(r, node.Status)
		if !ok {
			skipped = append(skipped, map[string]any{
				"check":       r.Name,
				"reason":      "no auto-fix mapped (manual remediation required)",
				"suggestions": r.Suggestions,
			})
			stillFailing = append(stillFailing, r)
			continue
		}
		if args.DryRun {
			healed = append(healed, map[string]any{
				"check":   r.Name,
				"action":  action.action,
				"result":  "dry_run",
				"message": action.message,
			})
			continue
		}
		err := mcpRunHealAction(ctx, tgt, node, action)
		entry := map[string]any{
			"check":  r.Name,
			"action": action.action,
		}
		if err != nil {
			entry["result"] = "failed"
			entry["message"] = err.Error()
			stillFailing = append(stillFailing, r)
		} else {
			entry["result"] = "succeeded"
			entry["message"] = action.message
			node.Status = "running"
			store.UpsertNode(st, *node)
			_ = store.Save(st)
		}
		healed = append(healed, entry)
	}

	return jsonResult(map[string]any{
		"name":          args.Name,
		"dry_run":       args.DryRun,
		"healed":        healed,
		"skipped":       skipped,
		"still_failing": stillFailing,
	})
}

type mcpHealAction struct {
	action  string
	message string
}

func mcpProposeHealAction(r diagnosis.CheckResult, nodeStatus string) (mcpHealAction, bool) {
	//nolint:gocritic // single case today, mirrors cmd.proposeHealAction.
	switch {
	case r.Name == "port_listening" && nodeStatus == "stopped":
		return mcpHealAction{
			action:  "start",
			message: "node was marked stopped in state; bringing it back up",
		}, true
	}
	return mcpHealAction{}, false
}

func mcpRunHealAction(ctx context.Context, tgt target.Target, node *state.ManagedNode, action mcpHealAction) error {
	rt := runtime.NewDockerRuntime(tgt, paths.Deployments())
	if action.action == "start" {
		return rt.Start(ctx, node.Name)
	}
	return fmt.Errorf("unknown heal action %q", action.action)
}

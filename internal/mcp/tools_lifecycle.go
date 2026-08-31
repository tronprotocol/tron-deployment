package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// registerLifecycleTools wires deploy-related tools. Most are
// destructive (the destructiveHint annotation prompts MCP-aware
// clients to confirm with the user before invoking).
//
// MCP-side note: we intentionally do NOT auto-run `apply` from
// `plan`. The agent decides whether to call apply based on the diff
// returned by plan, and only with auto_approve=true once the human
// approves.

type planArg struct {
	Path string `json:"path" jsonschema:"absolute path to an intent.yaml file"`
}

func registerLifecycleTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "plan",
		Title:       "Preview a deploy",
		Description: "Show the diff between the intent and the currently-deployed state, without executing. Returns `changes[]` (creates/updates), `destructive`, and `estimated_downtime_seconds`. Equivalent to `trond plan --intent <path> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, planTool)

	// `apply` is the most consequential tool we expose. Marked
	// destructive so MCP clients prompt the user. We do NOT pass
	// auto_approve=true by default — the LLM has to explicitly set it
	// after the user has approved the plan diff.
	mcp.AddTool(s, &mcp.Tool{
		Name:        "apply",
		Title:       "Deploy or update a node",
		Description: `Idempotent in-process deploy via internal/apply.Apply (the same pure function the CLI uses). Re-running with the same intent is a no-op (returns outcome="no_change"). With auto_approve=false (default), changes to an already-deployed node return error_code=HUMAN_REQUIRED — the agent must surface the diff (call 'plan' first), get user approval, then re-call 'apply' with auto_approve=true. With wait=true, blocks until the node's HTTP API responds 2xx or wait_timeout elapses (default 5m); a wait failure leaves the deploy successful but reports ready=false in the result. Equivalent to ` + "`trond apply --intent <path> --auto-approve -o json`" + `.`,
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrTrue(),
			IdempotentHint:  true,
		},
	}, applyTool)
}

func planTool(ctx context.Context, _ *mcp.CallToolRequest, args planArg) (*mcp.CallToolResult, any, error) {
	// MCP plan mirrors the CLI's state-aware create/no-op/update distinction.
	// It intentionally reports structural intent changes, while the CLI also
	// adds rendered-HOCON line diffs.
	parsed, err := intent.Load(args.Path)
	if err != nil {
		return errResult(err)
	}
	if len(parsed.Nodes) == 0 {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node"))
	}
	node := &parsed.Nodes[0]
	store, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error()))
	}
	intentBytes, readErr := os.ReadFile(args.Path)
	if readErr != nil {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, readErr.Error()))
	}
	existing := store.GetNode(st, parsed.Name)
	if parsed.Target.AutoPorts && existing != nil {
		rawParsed, rawErr := intent.LoadRaw(args.Path)
		if rawErr != nil {
			return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, rawErr.Error()))
		}
		if len(rawParsed.Nodes) == 0 {
			return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node"))
		}
		apply.RestoreAutoPorts(node, existing, &rawParsed.Nodes[0])
	}
	planned, err := apply.Plan(parsed, existing, intentBytes, apply.FindTemplatesDir())
	if err != nil {
		return errResult(err)
	}
	changes := make([]map[string]any, 0, len(planned.Changes))
	for _, c := range planned.Changes {
		changes = append(changes, map[string]any{"type": c.Type, "field": c.Field, "from": c.From, "to": c.To, "restart_required": c.RestartRequired})
	}
	return jsonResult(map[string]any{
		"name":                       parsed.Name,
		"current_state":              planned.CurrentState,
		"desired_state":              "running",
		"changes":                    changes,
		"destructive":                false,
		"estimated_downtime_seconds": planned.Downtime,
		"network":                    parsed.Network,
		"runtime":                    planned.Runtime,
		"node_count":                 len(parsed.Nodes),
		"first_node": map[string]any{
			"type":    node.Type,
			"version": node.Version,
			"memory":  node.Resources.Memory,
			"ports":   node.Ports,
		},
		"note": "MCP plan returns the same effective intent/config/version hash checks as CLI plan; line-level HOCON diff remains CLI-only.",
	})
}

type applyArgs struct {
	Path           string `json:"path" jsonschema:"absolute path to an intent.yaml file"`
	AutoApprove    bool   `json:"auto_approve,omitempty" jsonschema:"required to apply changes to an already-deployed node; otherwise the call returns HUMAN_REQUIRED"`
	Wait           bool   `json:"wait,omitempty" jsonschema:"block until the node's HTTP API responds"`
	RequirePrivate bool   `json:"require_private,omitempty" jsonschema:"refuse to apply unless the intent's network is private; returns PRIVATE_NETWORK_REQUIRED otherwise (the C1 safety gate, also forced on by the TROND_REQUIRE_PRIVATE env)"`
}

func applyTool(ctx context.Context, _ *mcp.CallToolRequest, args applyArgs) (*mcp.CallToolResult, any, error) {
	// Pure in-process apply via the internal/apply package. We
	// duplicate the cmd/apply.go pre-flight (load intent, resolve
	// target, lock, hash, HUMAN_REQUIRED gate) here because the MCP
	// surface is the structured tool, not a shell command.
	parsed, err := intent.Load(args.Path)
	if err != nil {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error()))
	}
	if len(parsed.Nodes) == 0 {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node"))
	}

	// --require-private (early gate, precedence): refuse a non-private intent
	// BEFORE target resolution and the HUMAN_REQUIRED change gate, so the
	// agent gets PRIVATE_NETWORK_REQUIRED (exit 2) rather than
	// TARGET_UNREACHABLE or HUMAN_REQUIRED masking it. Honors the tool arg
	// OR the TROND_REQUIRE_PRIVATE env floor.
	if err := guard.EnforceArg(args.RequirePrivate, parsed.Network); err != nil {
		return errResult(err)
	}
	// ...and against the network RECORDED IN STATE for the node this apply
	// would replace: the intent's `network:` is a caller-supplied label, so on
	// its own it let `name: <mainnet node>` + `network: private` re-deploy a
	// production node under the gate. Mirrors the CLI's requirePrivateForNode.
	if err := enforcePrivateForRecordedNode(args.RequirePrivate, parsed.Name); err != nil {
		return errResult(err)
	}

	tgt, err := resolveTarget(parsed)
	if err != nil {
		return errResult(output.NewError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error()))
	}
	if closer, ok := tgt.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	lock := state.NewLock(paths.BaseDir())
	if err := lock.Acquire(); err != nil {
		return errResult(output.NewError("LOCK_ERROR", output.ExitGeneralError, err.Error()))
	}
	defer lock.Release()

	store, st, err := state.Load(paths.State())
	if err != nil {
		return errResult(err)
	}

	existing := store.GetNode(st, parsed.Name)
	intentBytes, readErr := os.ReadFile(args.Path)
	if readErr != nil {
		return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, readErr.Error()))
	}
	if parsed.Target.AutoPorts && existing != nil {
		rawParsed, rawErr := intent.LoadRaw(args.Path)
		if rawErr != nil {
			return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, rawErr.Error()))
		}
		if len(rawParsed.Nodes) == 0 {
			return errResult(output.NewError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node"))
		}
		apply.RestoreAutoPorts(&parsed.Nodes[0], existing, &rawParsed.Nodes[0])
	}
	templateDir := apply.FindTemplatesDir()
	planned, err := apply.Plan(parsed, existing, intentBytes, templateDir)
	if err != nil {
		return errResult(err)
	}
	intentHash := planned.IntentHash
	legacyMatch := planned.LegacyMatch
	if legacyMatch && existing != nil {
		intentHash = existing.IntentHash
	}
	if existing != nil && existing.IntentHash != intentHash && !legacyMatch && !args.AutoApprove {
		return errResult(output.NewError("HUMAN_REQUIRED", output.ExitHumanRequired,
			fmt.Sprintf("Changes detected for node %q; pass auto_approve=true to proceed", parsed.Name)).
			WithSuggestions("Call the 'plan' tool first to inspect the diff",
				"Surface the diff to the user, get approval, then re-call apply with auto_approve=true"))
	}

	res, err := apply.Apply(ctx, apply.Options{
		Intent:         parsed,
		Target:         tgt,
		Store:          store,
		State:          st,
		IntentHash:     intentHash,
		Existing:       existing,
		TemplateDir:    templateDir,
		DeploymentsDir: paths.Deployments(),
		EnvVars:        apply.ResolveEnvVars(&parsed.Nodes[0]),
		Wait:           args.Wait,
		WaitTimeout:    5 * time.Minute,
		RequirePrivate: args.RequirePrivate || guard.Requested(),
	})
	if err != nil {
		// Pass a structured error (e.g. the core PRIVATE_NETWORK_REQUIRED
		// gate) through with its own code/exit; only opaque errors get the
		// generic DEPLOY_ERROR wrap. Without this, threading RequirePrivate
		// into the core would surface as DEPLOY_ERROR and lose the contract.
		var se *output.StructuredError
		if errors.As(err, &se) {
			return errResult(se)
		}
		return errResult(output.NewError("DEPLOY_ERROR", output.ExitGeneralError, err.Error()))
	}
	return jsonResult(res)
}

// enforcePrivateForRecordedNode is the MCP twin of cmd/resolve.go's
// requirePrivateForNode: it enforces the --require-private gate against the
// RECORDED network of an already-deployed node, using state ONLY — no target
// resolution, so an unreachable mainnet node still refuses with
// PRIVATE_NETWORK_REQUIRED. requested is the tool's own require_private
// argument, OR-ed with the flag/env floor inside guard. A node that is not in
// state returns nil: a fresh deploy is governed by the intent-side check.
// Fast path: no state read at all when the gate is off.
func enforcePrivateForRecordedNode(requested bool, name string) error {
	if !requested && !guard.Requested() {
		return nil
	}
	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}
	st, err := store.Load()
	if err != nil {
		return err
	}
	node := store.GetNode(st, name)
	if node == nil {
		return nil
	}
	return guard.EnforceArg(requested, node.Network)
}

// resolveTarget mirrors cmd/apply.go's helper. Duplicated here so the
// internal/mcp package doesn't import cmd/. Limited to local + ssh
// since those are the only two intent.Target.Type values.
func resolveTarget(parsed *intent.Intent) (target.Target, error) {
	return target.FromIntent(parsed)
}

func ptrTrue() *bool { v := true; return &v }

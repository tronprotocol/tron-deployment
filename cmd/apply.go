package cmd

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var (
	applyIntentPath  string
	applyAutoApprove bool
	applyWait        bool
	applyWaitTimeout time.Duration
	applyMonitor     bool
)

var applyCmd = &cobra.Command{
	Use:     "apply",
	Aliases: []string{"deploy"},
	Short:   "Deploy or update a node from an intent file",
	Long: `Apply deploys a node to the specified target based on the intent file.

Pipeline: validate → resolve target → acquire lock → render config →
diff against state → deploy → update state → release lock → output result.

This command is idempotent — running it again with the same intent produces no changes.

The deploy phase (render → runtime → state save → optional wait) lives in
the internal/apply package as a pure function so MCP and recipe callers
can drive the same pipeline without forking a subprocess.`,
	RunE: runApply,
}

func init() {
	applyCmd.Flags().StringVar(&applyIntentPath, "intent", "", "Path to intent.yaml (required)")
	applyCmd.Flags().BoolVar(&applyAutoApprove, "auto-approve", false, "Skip confirmation for changes (CI mode)")
	applyCmd.Flags().BoolVar(&applyWait, "wait", false, "Block until the deployed node's HTTP API is reachable")
	applyCmd.Flags().DurationVar(&applyWaitTimeout, "wait-timeout", 5*time.Minute, "Total wait budget when --wait is set")
	applyCmd.Flags().BoolVar(&applyMonitor, "monitor", false, "Deploy monitoring stack (Prometheus + Grafana) alongside the node")
	// --require-private is the persistent root flag (see cmd/root.go) +
	// TROND_REQUIRE_PRIVATE env, enforced via internal/guard — apply
	// inherits it like every other verb, so no per-command flag here.
	mustMarkRequired(applyCmd, "intent")
	mustMarkRequired(applyCmd, "intent")
	rootCmd.AddCommand(applyCmd)
}

func runApply(cmd *cobra.Command, args []string) error {
	start := time.Now()

	// 1. Load + validate intent.
	parsed, err := intent.Load(applyIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error(),
			"Check intent file syntax", "Run: trond config validate "+applyIntentPath)
	}

	// --require-private (early gate, precedence). The authoritative
	// enforcement lives in apply.Apply() core so EVERY path inherits it,
	// but we ALSO check here, first — before target resolution and the
	// HUMAN_REQUIRED change-detection gate — so a non-private intent
	// refuses with PRIVATE_NETWORK_REQUIRED (exit 2) rather than being
	// masked by HUMAN_REQUIRED (exit 10). The predicate + error live in
	// internal/guard (shared with every mutator); this is layered defense.
	if err := guard.Enforce(parsed.Network); err != nil {
		return err
	}
	// ...and against the network RECORDED IN STATE for the node this apply
	// would replace. The intent's `network:` is a caller-supplied label; when
	// a node already exists under this name, the gate must be decided by what
	// is actually deployed — otherwise `name: <mainnet node>` +
	// `network: private` walks straight through. State-only, so an
	// unreachable mainnet node still refuses with PRIVATE_NETWORK_REQUIRED
	// instead of TARGET_UNREACHABLE. A missing node returns nil (a fresh
	// deploy is governed by the intent check above). apply.Apply repeats this
	// under the state lock — this is the precedence layer.
	if err := requirePrivateForNode(parsed.Name); err != nil {
		return err
	}

	// Monitoring is opt-in: only deploy when --monitor is explicitly passed.
	if cmd.Flags().Changed("monitor") {
		if parsed.Monitoring == nil {
			parsed.Monitoring = &intent.Monitoring{}
		}
		parsed.Monitoring.Enabled = intent.BoolPtr(true)
		intent.ApplyMonitoringDefaults(parsed.Monitoring)
	} else if parsed.Monitoring != nil && parsed.Monitoring.Enabled != nil && *parsed.Monitoring.Enabled {
		// Intent says enabled=true, but no --monitor flag: disable.
		parsed.Monitoring.Enabled = intent.BoolPtr(false)
	}

	// 2. Resolve target. SSH cert handling lives here in the cmd
	// layer because it's tied to the operator's shell environment.
	tgt, err := resolveTarget(parsed)
	if err != nil {
		return exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error(),
			"Check SSH connectivity", "Verify Docker is running")
	}

	// 3. Acquire state lock. Stays in cmd for the defer-release shape.
	dir := stateDir()
	lock := state.NewLock(dir)
	if err := lock.Acquire(); err != nil {
		return exitWithError("LOCK_ERROR", output.ExitGeneralError, "Failed to acquire state lock: "+err.Error(),
			"Check if another trond process is running")
	}
	defer lock.Release()

	// 4. Load current state.
	store, err := state.NewStore(statePath())
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	deployState, err := store.Load()
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	// 5. Compute intent hash.
	intentData, _ := os.ReadFile(applyIntentPath)
	intentHash := apply.IntentHashFromBytes(intentData)

	// 6. HUMAN_REQUIRED gate. The internal/apply package handles the
	// no-op short-circuit (same-hash → "no_change") on its own; we
	// only need to guard the destructive change-on-existing-node case.
	existing := store.GetNode(deployState, parsed.Name)
	if existing != nil && existing.IntentHash != intentHash && !applyAutoApprove {
		return exitWithError("HUMAN_REQUIRED", output.ExitHumanRequired,
			fmt.Sprintf("Changes detected for node %q. Review with: trond plan --intent %s", parsed.Name, applyIntentPath),
			"Re-run with --auto-approve to apply changes",
			fmt.Sprintf("trond apply --intent %s --auto-approve", applyIntentPath))
	}

	// 7. Hand off to the pure deploy phase.
	res, err := apply.Apply(cmd.Context(), apply.Options{
		Intent:         parsed,
		Target:         tgt,
		Store:          store,
		State:          deployState,
		IntentHash:     intentHash,
		Existing:       existing,
		TemplateDir:    findTemplatesDir(),
		DeploymentsDir: deploymentsDir(),
		EnvVars:        resolveEnvVars(&parsed.Nodes[0]),
		IntentPath:     applyIntentPath, // FR-021: relative build.source resolves vs this
		Wait:           applyWait,
		WaitTimeout:    applyWaitTimeout,
		RequirePrivate: guard.Requested(),
	})
	if err != nil {
		return wrapApplyError(err)
	}

	// 8. Translate Result back into the JSON shape the CLI promises.
	// Field names match schemas/output/apply.schema.json + AGENTS.md:
	// {name, result, intent_hash, endpoints, duration_ms, ready,
	// runtime, version}. The internal/apply.Result uses "outcome" as
	// the in-Go name; we surface it as "result" on the wire because
	// that's what the public contract uses.
	durationMs := time.Since(start).Milliseconds()
	resultMap := applyResultMap(res, durationMs)

	writeAudit(auditEvent{
		Command:    "apply",
		Node:       parsed.Name,
		Target:     tgt.String(),
		IntentHash: intentHash,
		Result:     "success",
		Start:      start,
	})

	if applyWait {
		resultMap["waited_ms"] = res.WaitedMs
		if res.WaitError != "" {
			resultMap["ready"] = false
			resultMap["wait_error"] = res.WaitError
			if !quiet {
				writeResult(resultMap)
			}
			return output.NewError("WAIT_TIMEOUT", output.ExitGeneralError,
				fmt.Sprintf("deploy succeeded but node %s did not become ready: %s", parsed.Name, res.WaitError)).
				WithSuggestions("trond logs "+parsed.Name, "trond diagnose "+parsed.Name)
		}
		resultMap["ready"] = true
	}

	if !quiet {
		writeResult(resultMap)
	}
	return nil
}

func resolveTarget(parsed *intent.Intent) (target.Target, error) {
	switch parsed.Target.Type {
	case "ssh":
		t := target.NewSSHTarget(parsed.Target.Host, parsed.Target.Port, parsed.Target.User, parsed.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return nil, err
		}
		return t, nil
	default:
		return target.NewLocalTarget(), nil
	}
}

func resolveEnvVars(node *intent.NodeSpec) map[string]string {
	env := make(map[string]string)
	if node.WitnessKeyEnv != "" {
		val := os.Getenv(node.WitnessKeyEnv)
		if val != "" {
			env[node.WitnessKeyEnv] = val
		}
	}
	return env
}

// findTemplatesDir returns an optional on-disk templates directory.
// Empty return signals render to use the embedded copy.
func findTemplatesDir() string {
	if d := os.Getenv("TROND_TEMPLATES_DIR"); d != "" {
		return d
	}
	candidates := []string{"templates", "./templates"}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			if _, err := os.Stat(c + "/main_net_config.conf"); err == nil {
				return c
			}
		}
	}
	return ""
}

// exitWithError returns a StructuredError for propagation through cobra RunE.
func exitWithError(code string, exitCode int, msg string, suggestions ...string) error {
	return output.NewError(code, exitCode, msg).WithSuggestions(suggestions...)
}

// applyResultMap translates an apply.Result into the JSON map the CLI
// emits for `apply -o json`. Extracted from runApply so the wire shape
// is unit-testable WITHOUT a live deploy — the original inline map
// silently dropped network/is_private (the struct had them, the map
// didn't), and a struct-level test couldn't catch it. Field names match
// schemas/output/apply.schema.json. Wait fields (ready/waited_ms) are
// layered on by runApply since they depend on the --wait flag.
func applyResultMap(res *apply.Result, durationMs int64) map[string]any {
	m := map[string]any{
		"name":        res.Name,
		"result":      res.Outcome,
		"intent_hash": res.IntentHash,
		"runtime":     res.Runtime,
		"version":     res.Version,
		"network":     res.Network,
		"is_private":  res.IsPrivate,
		"endpoints":   res.Endpoints,
		"duration_ms": durationMs,
	}
	if res.ConfigHash != "" {
		m["config_hash"] = res.ConfigHash
	}
	if res.Build != nil {
		m["build"] = res.Build
	}
	if res.MonitoringError != "" {
		m["monitoring_error"] = res.MonitoringError
	}
	if len(res.MonitoringEndpoints) > 0 {
		m["monitoring"] = res.MonitoringEndpoints
	}
	return m
}

// writeResult emits result as JSON on stdout. The output format is
// always JSON for now — text-mode output is rendered by each
// command's RunE before reaching this helper.
func writeResult(result any) {
	output.WriteJSON(os.Stdout, result)
}

// wrapApplyError decides whether an error from apply.Apply needs
// wrapping for the user-facing error envelope. Errors that are
// already *output.StructuredError (BUILD_FAILED, INVALID_SOURCE,
// BUILD_CANCELLED, VALIDATION_ERROR, etc.) propagate as-is so the
// agent sees the correct error_code + exit_code. Everything else
// (raw fmt.Errorf from the deploy plumbing) becomes a generic
// DEPLOY_ERROR.
//
// Extracted so the wrap/pass-through decision is unit-testable
// without spinning up a full cobra apply path.
func wrapApplyError(err error) error {
	if err == nil {
		return nil
	}
	var se *output.StructuredError
	if errors.As(err, &se) {
		return se
	}
	return output.NewError("DEPLOY_ERROR", output.ExitGeneralError, err.Error()).
		WithSuggestions(
			"Check Docker is running: docker info",
			"Check port availability",
		)
}

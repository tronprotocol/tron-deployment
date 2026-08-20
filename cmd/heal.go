package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

// `trond heal <node>` is the auto-fix counterpart to `trond diagnose`.
// It runs the same diagnostic suite and, for each failed check whose
// remediation is *known and safe*, applies the fix automatically.
// Anything destructive (remove, network destroy, --force) stays in
// human hands.
//
// Conservative by design: a v1 healer that auto-restarts on every
// fail signal would run away — sync_progress=fail just means "wait
// longer," not "restart." We map specific (check, current state)
// tuples to specific actions and explicitly skip the rest, surfacing
// suggestions[] so the operator can pick up where heal stopped.
//
// Output schema: schemas/output/heal.schema.json.
//
// Idempotent: safe to re-run. If the previous run fixed everything,
// the next one returns healed=[] / skipped=[] / still_failing=[].

var (
	healDryRun bool
	healOnly   []string // restrict to specific check names; defaults = all
)

var autoHealCmd = &cobra.Command{
	Use:   "auto-heal <node>",
	Short: "Run diagnose, then auto-fix the failures whose remediation is known + safe",
	Long: `Heal walks trond's diagnose output and attempts the documented
remediation for each fail. Read-only inspection (status, diagnose);
destructive actions stay behind a HUMAN_REQUIRED gate.

Currently auto-fixable:
  port_listening=fail and node.status=stopped → trond start <node>

Surfaced for human action (heal does NOT touch them):
  sync_progress=fail   — node is alive, just behind. Wait.
  peer_count=fail      — recovers on its own when peers come back.
  disk_space=fail      — needs operator attention.
  memory_usage=fail    — needs operator attention.

Use --dry-run to see what heal would do without acting.`,
	Args: cobra.ExactArgs(1),
	RunE: runAutoHeal,
}

func init() {
	autoHealCmd.Flags().BoolVar(&healDryRun, "dry-run", false,
		"Print proposed actions without executing them")
	autoHealCmd.Flags().StringSliceVar(&healOnly, "only", nil,
		"Comma-separated check names to consider; default = all checks")
	rootCmd.AddCommand(autoHealCmd)
}

// healAction is one auto-fix attempt: which check triggered it,
// what action we ran, and whether it succeeded.
type healAction struct {
	Check   string `json:"check"`
	Action  string `json:"action"`
	Result  string `json:"result"` // succeeded | failed | dry_run
	Message string `json:"message,omitempty"`
}

// healSkip records a fail check we intentionally didn't auto-fix
// (with the reason). Surfaces the suggestions[] so the operator
// has the same context the agent would.
type healSkip struct {
	Check       string   `json:"check"`
	Reason      string   `json:"reason"`
	Suggestions []string `json:"suggestions,omitempty"`
}

func runAutoHeal(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	// --require-private / TROND_REQUIRE_PRIVATE (the C1 gate): auto-heal is a
	// mutator on this path — executeHealAction calls Runtime.Start and rewrites
	// state — so it must refuse a non-private node exactly like `trond start`.
	// State-only and BEFORE resolveNodeContext, so an unreachable mainnet node
	// still returns PRIVATE_NETWORK_REQUIRED rather than TARGET_UNREACHABLE.
	//
	// Scoped to the acting path: with --dry-run the loop below `continue`s
	// before executeHealAction, so nothing is started and no state is written.
	// The preview stays available under the gate — it is how an operator (or
	// agent) sees what heal *would* do without being allowed to do it.
	if !healDryRun {
		if err := requirePrivateForNode(name); err != nil {
			return err
		}
	}

	nc, err := resolveNodeContextForWrite(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Run the same checker matrix `trond diagnose` does.
	opts := diagnosis.CheckOpts{
		NodeName:    nc.Node.Name,
		NodeType:    "", // diagnose doesn't read this from state today; checkers cope
		Runtime:     nc.Node.Runtime,
		HTTPPort:    nc.Node.HTTPPort,
		GRPCPort:    nc.Node.GRPCPort,
		InstallPath: nc.Node.InstallPath,
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	checkers := diagnosis.AllCheckers()
	results := make([]diagnosis.CheckResult, 0, len(checkers))
	for _, c := range checkers {
		if len(healOnly) > 0 && !contains(healOnly, c.Name()) {
			continue
		}
		results = append(results, c.Run(ctx, nc.Target, opts))
	}

	var (
		healed       []healAction
		skipped      []healSkip
		stillFailing []diagnosis.CheckResult
	)

	for _, r := range results {
		if r.Status != diagnosis.StatusFail {
			continue
		}
		action, ok := proposeHealAction(r, nc.Node.Status)
		if !ok {
			skipped = append(skipped, healSkip{
				Check:       r.Name,
				Reason:      "no auto-fix mapped (manual remediation required)",
				Suggestions: r.Suggestions,
			})
			stillFailing = append(stillFailing, r)
			continue
		}
		if healDryRun {
			healed = append(healed, healAction{
				Check:   r.Name,
				Action:  action.Action,
				Result:  "dry_run",
				Message: action.Message,
			})
			continue
		}
		if err := executeHealAction(cmd.Context(), nc, action); err != nil {
			healed = append(healed, healAction{
				Check:   r.Name,
				Action:  action.Action,
				Result:  "failed",
				Message: err.Error(),
			})
			stillFailing = append(stillFailing, r)
			continue
		}
		healed = append(healed, healAction{
			Check:   r.Name,
			Action:  action.Action,
			Result:  "succeeded",
			Message: action.Message,
		})
	}

	result := map[string]any{
		"name":          name,
		"healed":        healed,
		"skipped":       skipped,
		"still_failing": stillFailing,
		"duration_ms":   time.Since(start).Milliseconds(),
		"dry_run":       healDryRun,
	}

	outputFmt, _ := cmd.Flags().GetString("output")
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, result)
	}
	if len(healed) == 0 && len(skipped) == 0 {
		fmt.Printf("✓ %s: no failed checks; nothing to heal.\n", name)
		return nil
	}
	for _, h := range healed {
		fmt.Printf("[%s] %s → %s: %s\n", h.Result, h.Check, h.Action, h.Message)
	}
	for _, s := range skipped {
		fmt.Printf("[skipped] %s: %s\n", s.Check, s.Reason)
		for _, sg := range s.Suggestions {
			fmt.Printf("    - %s\n", sg)
		}
	}
	return nil
}

// proposeHealAction maps (check.Name, current state) tuples to a
// concrete fix. Returns ok=false when no automatic remediation is
// safe — the caller surfaces suggestions[] instead.
//
// Adding a new auto-fix means landing both:
//
//  1. A case here that returns ok=true plus a healAction definition.
//  2. A test in cmd/heal_test.go pinning the (check, state) tuple
//     so the mapping doesn't silently drift.
func proposeHealAction(r diagnosis.CheckResult, nodeStatus string) (proposedAction, bool) {
	// Switch (rather than if-else) so future cases land cleanly:
	// each (check, state) tuple gets one case + one test row.
	//nolint:gocritic // single-case today; will grow per the package doc above.
	switch {
	case r.Name == "port_listening" && nodeStatus == "stopped":
		return proposedAction{
			Action:  "start",
			Message: "node was marked stopped in state; bringing it back up",
		}, true
	}
	return proposedAction{}, false
}

type proposedAction struct {
	Action  string // "start" | "restart" | future actions
	Message string
}

// executeHealAction runs the proposed action against the node. We
// reuse the existing trond commands' machinery rather than calling
// docker / systemd directly so audit logs + state updates flow
// through the same path a manual `trond start` would.
func executeHealAction(ctx context.Context, nc *nodeContext, action proposedAction) error {
	if action.Action == "start" {
		// Mirror cmd/start.go without going through cobra. The
		// runtime's Start handles docker compose start / systemctl
		// start uniformly.
		if err := nc.Runtime.Start(ctx, nc.Node.Name); err != nil {
			return err
		}
		nc.Node.Status = "running"
		return nc.SaveState()
	}
	return fmt.Errorf("unknown heal action %q", action.Action)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

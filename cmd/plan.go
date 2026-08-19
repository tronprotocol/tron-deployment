package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

var (
	planIntentPath string
	planShowDiff   bool
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Preview deployment changes without applying",
	Long: `Show what trond apply would do: validate, render, diff against
current state, output changes.

By default the diff is field-level (intent_hash / config_hash /
version deltas). Pass --diff to also surface the line-by-line HOCON
content diff so reviewers can see WHICH config keys would actually
change, not just that the hash drifted.`,
	RunE: runPlan,
}

func init() {
	planCmd.Flags().StringVar(&planIntentPath, "intent", "", "Path to intent.yaml (required)")
	planCmd.Flags().BoolVar(&planShowDiff, "diff", false,
		"Include the line-by-line HOCON config diff in the output (text + JSON)")
	mustMarkRequired(planCmd, "intent")
	rootCmd.AddCommand(planCmd)
}

type planChange struct {
	Type            string `json:"type"`
	Field           string `json:"field"`
	From            any    `json:"from,omitempty"`
	To              any    `json:"to,omitempty"`
	RestartRequired bool   `json:"restart_required"`
}

func runPlan(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	// 1. Load + validate
	parsed, err := intent.Load(planIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// 2. Compute intent hash
	intentData, _ := os.ReadFile(planIntentPath)
	intentHash := apply.IntentHashFromBytes(intentData)

	// 3. Load current state
	store, err := state.NewStore(statePath())
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	deployState, err := store.Load()
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	existing := store.GetNode(deployState, parsed.Name)

	// 4. Render config to compute config hash
	templateDir := findTemplatesDir()
	node := &parsed.Nodes[0]

	rendered, err := render.RenderHOCONWithSecrets(templateDir, parsed, node)
	if err != nil {
		return exitWithError("RENDER_ERROR", output.ExitGeneralError, err.Error())
	}
	// The REAL bytes: config_hash must match what apply deploys, and the
	// diff below must compare real values so a rotated witness key is
	// still detected. Nothing from here reaches the output un-redacted —
	// simpleHOCONDiff redacts every line at the point it emits it.
	hoconConfig := rendered.Deployable()
	configHash := apply.IntentHashFromBytes([]byte(hoconConfig))

	// 5. Diff
	var changes []planChange
	destructive := false
	downtime := 0

	if existing == nil {
		// New deployment
		changes = append(changes, planChange{
			Type:  "create",
			Field: "node",
			To:    parsed.Name,
		})
	} else {
		// Check for changes
		if existing.IntentHash != intentHash {
			changes = append(changes, planChange{
				Type:            "update",
				Field:           "intent",
				From:            existing.IntentHash[:12] + "...",
				To:              intentHash[:12] + "...",
				RestartRequired: true,
			})
		}
		if existing.ConfigHash != configHash {
			changes = append(changes, planChange{
				Type:            "update",
				Field:           "config",
				From:            existing.ConfigHash[:12] + "...",
				To:              configHash[:12] + "...",
				RestartRequired: true,
			})
			downtime = 30 // Estimated restart time
		}
		if existing.Version != node.Version && node.Version != "latest" {
			changes = append(changes, planChange{
				Type:            "update",
				Field:           "version",
				From:            existing.Version,
				To:              node.Version,
				RestartRequired: true,
			})
			downtime = 60
		}
	}

	currentState := "not deployed"
	if existing != nil {
		currentState = existing.Status
	}

	runtimeType := parsed.Target.Runtime
	if runtimeType == "" {
		runtimeType = "docker"
	}

	result := map[string]any{
		"name":                       parsed.Name,
		"current_state":              currentState,
		"desired_state":              "running",
		"changes":                    changes,
		"destructive":                destructive,
		"estimated_downtime_seconds": downtime,
		"runtime":                    runtimeType,
		"network":                    parsed.Network,
	}

	// --diff: surface the line-by-line HOCON content diff so reviewers
	// can see which keys actually changed, not just that hashes drifted.
	// Skipped when there's no deployed config to compare against
	// (existing == nil) or when the on-disk deployed file is missing
	// (deployment dir cleaned, etc.).
	var diffLines []string
	if planShowDiff && existing != nil {
		deployedPath := filepath.Join(paths.Deployments(), parsed.Name, parsed.Name+".conf")
		if data, err := os.ReadFile(deployedPath); err == nil {
			diffLines = simpleHOCONDiff(strings.Split(string(data), "\n"),
				strings.Split(hoconConfig, "\n"))
		}
		// JSON consumers always get the field (possibly empty array)
		// so they can distinguish "no changes" from "diff was not
		// requested" by checking whether the key is present.
		result["config_diff"] = diffLines
	}

	if outputFmt == "json" {
		output.WriteJSON(os.Stdout, result)
	} else {
		printPlanText(result, changes)
		if planShowDiff {
			printDiffSection(existing, diffLines)
		}
	}

	return nil
}

// simpleHOCONDiff is a deliberately tiny line-by-line differ. Same
// shape as cmd/config/diff.go::simpleDiff but private to plan to
// avoid coupling — both could call into a shared internal/diff
// helper later if a third caller appears.
//
// Secret handling: the comparison runs on the raw lines (so a witness
// key that actually changed is still reported as drift), but every
// line is passed through render.RedactWitnessLine before it is
// emitted. This differ is positional and LCS-free, so a line-count
// change anywhere above the `localwitness` assignment misaligns the
// tail and would otherwise push the SR private key straight into
// stdout and into result["config_diff"].
func simpleHOCONDiff(old, new []string) []string {
	// Redact whole-slice: a multi-line `localwitness = [` array keeps its
	// key on a line that does not itself start with the key name.
	oldR := render.RedactWitnessLines(old)
	newR := render.RedactWitnessLines(new)
	var diffs []string
	maxLen := len(old)
	if len(new) > maxLen {
		maxLen = len(new)
	}
	for i := 0; i < maxLen; i++ {
		var oldLine, newLine string
		if i < len(old) {
			oldLine = old[i]
		}
		if i < len(new) {
			newLine = new[i]
		}
		if oldLine != newLine {
			if oldLine != "" {
				diffs = append(diffs, "- "+oldR[i])
			}
			if newLine != "" {
				diffs = append(diffs, "+ "+newR[i])
			}
		}
	}
	return diffs
}

// printDiffSection renders the line diff under the changes section
// in text mode. Stays empty when there's nothing useful to say.
func printDiffSection(existing *state.ManagedNode, diffLines []string) {
	switch {
	case existing == nil:
		fmt.Println("\nConfig diff: (skipped — node not yet deployed)")
	case len(diffLines) == 0:
		fmt.Println("\nConfig diff: (no rendered HOCON differences)")
	default:
		fmt.Printf("\nConfig diff (%d line(s)):\n", len(diffLines))
		for _, line := range diffLines {
			fmt.Println("  " + line)
		}
	}
}

func printPlanText(result map[string]any, changes []planChange) {
	fmt.Printf("Node:    %s\n", result["name"])
	fmt.Printf("Current: %s\n", result["current_state"])
	fmt.Printf("Desired: %s\n", result["desired_state"])
	fmt.Printf("Runtime: %s\n", result["runtime"])
	fmt.Println()

	if len(changes) == 0 {
		fmt.Println("No changes. Infrastructure is up-to-date.")
		return
	}

	fmt.Printf("Changes (%d):\n", len(changes))
	for _, c := range changes {
		switch c.Type {
		case "create":
			fmt.Printf("  + %s: %v\n", c.Field, c.To)
		case "update":
			fmt.Printf("  ~ %s: %v → %v", c.Field, c.From, c.To)
			if c.RestartRequired {
				fmt.Print(" (restart required)")
			}
			fmt.Println()
		case "delete":
			fmt.Printf("  - %s: %v\n", c.Field, c.From)
		}
	}

	if dt, ok := result["estimated_downtime_seconds"].(int); ok && dt > 0 {
		fmt.Printf("\nEstimated downtime: %ds\n", dt)
	}
}

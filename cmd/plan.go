package cmd

import (
	"fmt"
	"os"
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

type planChange = apply.PlanChange

func runPlan(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	// 1. Load + validate
	parsed, err := intent.Load(planIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}
	if len(parsed.Nodes) == 0 {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node")
	}

	// 2. Load current state
	store, err := state.NewStore(statePath())
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	deployState, err := store.Load()
	if err != nil {
		return exitWithError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	existing := store.GetNode(deployState, parsed.Name)
	if parsed.Target.AutoPorts && existing != nil {
		rawParsed, rawErr := intent.LoadRaw(planIntentPath)
		if rawErr != nil {
			return exitWithError("VALIDATION_ERROR", output.ExitValidationError, rawErr.Error())
		}
		if len(rawParsed.Nodes) == 0 {
			return exitWithError("VALIDATION_ERROR", output.ExitValidationError, "intent must contain at least one node")
		}
		apply.RestoreAutoPorts(&parsed.Nodes[0], existing, &rawParsed.Nodes[0])
	}
	rawIntent, err := os.ReadFile(planIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}
	planned, err := apply.Plan(parsed, existing, rawIntent, apply.FindTemplatesDir())
	if err != nil {
		return err
	}

	result := map[string]any{
		"name":                       parsed.Name,
		"current_state":              planned.CurrentState,
		"desired_state":              "running",
		"changes":                    planned.Changes,
		"destructive":                false,
		"estimated_downtime_seconds": planned.Downtime,
		"runtime":                    planned.Runtime,
		"network":                    parsed.Network,
	}

	// --diff: surface the line-by-line HOCON content diff so reviewers
	// can see which keys actually changed, not just that hashes drifted.
	// Skipped when there's no deployed config to compare against
	// (existing == nil) or when the on-disk deployed file is missing
	// (deployment dir cleaned, etc.).
	var diffLines []string
	if planShowDiff && existing != nil {
		deployedPath := paths.DeploymentConfig(parsed.Name)
		if data, err := os.ReadFile(deployedPath); err == nil {
			diffLines = render.DiffLines(strings.Split(string(data), "\n"),
				strings.Split(string(planned.Config), "\n"), 0)
		}
		// JSON consumers always get the field (possibly empty array)
		// so they can distinguish "no changes" from "diff was not
		// requested" by checking whether the key is present.
		result["config_diff"] = diffLines
	}

	if outputFmt == "json" {
		output.WriteJSON(os.Stdout, result)
	} else {
		printPlanText(result, planned.Changes)
		if planShowDiff {
			printDiffSection(existing, diffLines)
		}
	}

	return nil
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

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/diagnosis"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

var diagnoseCheckers = diagnosis.AllCheckers

var diagnoseCmd = &cobra.Command{
	Use:   "diagnose <node>",
	Short: "Run diagnostic checks on a node",
	Long:  "Run all diagnostic checks and return a structured health report with fix suggestions.",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiagnose,
}

func init() {
	rootCmd.AddCommand(diagnoseCmd)
}

func runDiagnose(cmd *cobra.Command, args []string) error {
	name := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")

	nc, err := resolveNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	opts := diagnosis.CheckOpts{
		NodeName: nc.Node.Name,
		Runtime:  nc.Node.Runtime,
		Network:  nc.Node.Network,
		HTTPPort: nc.Node.HTTPPort,
		GRPCPort: nc.Node.GRPCPort,
	}

	checkers := diagnoseCheckers()
	var results []diagnosis.CheckResult

	for _, checker := range checkers {
		result := checker.Run(cmd.Context(), nc.Target, opts)
		results = append(results, result)
	}

	overall := diagnosis.OverallStatus(results)

	report := map[string]any{
		"name":    name,
		"overall": overall,
		"checks":  results,
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, report)
	}

	// Text output
	fmt.Printf("Diagnosis: %s\n", name)
	fmt.Printf("Overall:   %s\n\n", overall)

	for _, r := range results {
		icon := "✓"
		if r.Status == diagnosis.StatusFail {
			icon = "✗"
		} else if r.Status == diagnosis.StatusWarning {
			icon = "⚠"
		}
		fmt.Printf("%s %-20s %s\n", icon, r.Name, r.Message)
		for _, s := range r.Suggestions {
			fmt.Printf("  → %s\n", s)
		}
	}

	return nil
}

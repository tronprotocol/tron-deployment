package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/render"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

var diffCmd = &cobra.Command{
	Use:   "diff <intent-path>",
	Short: "Show differences between rendered and deployed config",
	Args:  cobra.ExactArgs(1),
	RunE:  runDiff,
}

func init() {
	Cmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) error {
	intentPath := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")

	parsed, err := intent.Load(intentPath)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// Render new config
	templateDir := findTemplateDir()
	node := &parsed.Nodes[0]

	rendered, err := render.RenderHOCONWithSecrets(templateDir, parsed, node)
	if err != nil {
		return output.NewError("RENDER_ERROR", output.ExitGeneralError, err.Error())
	}
	// Compare the REAL bytes against the deployed file so a rotated
	// witness key still shows up as a difference; simpleDiff redacts
	// each line as it emits it, so no key reaches stdout or the JSON.
	newConfig := rendered.Deployable()

	// Load deployed config from state
	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}
	deployState, err := store.Load()
	if err != nil {
		return err
	}

	existing := store.GetNode(deployState, parsed.Name)
	if existing == nil {
		if outputFmt == "json" {
			output.WriteJSON(os.Stdout, map[string]any{
				"name":    parsed.Name,
				"status":  "new",
				"message": "Node not yet deployed; entire config is new",
			})
		} else {
			fmt.Printf("Node %q not yet deployed. Entire config will be new.\n", parsed.Name)
		}
		return nil
	}

	// Try to read the deployed config
	deployedConfigPath := filepath.Join(paths.Deployments(), parsed.Name, parsed.Name+".conf")
	deployedData, err := os.ReadFile(deployedConfigPath)
	if err != nil {
		if outputFmt == "json" {
			output.WriteJSON(os.Stdout, map[string]any{
				"name":    parsed.Name,
				"status":  "unknown",
				"message": "Could not read deployed config for comparison",
			})
		} else {
			fmt.Printf("Could not read deployed config at %s\n", deployedConfigPath)
		}
		return nil
	}

	oldLines := strings.Split(string(deployedData), "\n")
	newLines := strings.Split(newConfig, "\n")

	diffs := simpleDiff(oldLines, newLines)

	if outputFmt == "json" {
		output.WriteJSON(os.Stdout, map[string]any{
			"name":        parsed.Name,
			"has_changes": len(diffs) > 0,
			"diff_count":  len(diffs),
			"diffs":       diffs,
		})
	} else {
		if len(diffs) == 0 {
			fmt.Println("No config differences.")
		} else {
			for _, d := range diffs {
				fmt.Println(d)
			}
		}
	}

	return nil
}

// simpleDiff does a basic line-by-line comparison.
//
// Secret handling: comparison uses the raw lines so genuine drift is
// still reported, but every emitted line goes through
// render.RedactWitnessLine first. The comparison is positional and
// LCS-free, so any line-count change above the `localwitness`
// assignment misaligns the tail and would otherwise print the SR
// private key into `diffs[]`.
func simpleDiff(old, new []string) []string {
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
				diffs = append(diffs, fmt.Sprintf("- %s", render.RedactWitnessLine(oldLine)))
			}
			if newLine != "" {
				diffs = append(diffs, fmt.Sprintf("+ %s", render.RedactWitnessLine(newLine)))
			}
		}
	}

	return diffs
}

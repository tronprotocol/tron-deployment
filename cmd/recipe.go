package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/recipe"
)

// recipeCmd is the parent for `trond recipe list / show / run`.
// Recipes are pre-built declarative workflows codified from AGENTS.md
// (deploy + verify, snapshot then deploy, recover from failed upgrade,
// destroy a network, upgrade with auto-rollback). Each recipe is one
// YAML file in the embedded recipes/ directory.
//
// The runner re-execs the trond binary itself for each step. This
// keeps every step idempotent and testable in isolation; the runner
// has no knowledge of any specific subcommand's semantics, only of
// step ordering / param substitution / on_failure handling /
// rollback.
var recipeCmd = &cobra.Command{
	Use:   "recipe",
	Short: "Run pre-built declarative trond workflows",
	Long: `Recipes codify the canonical multi-step workflows from AGENTS.md as
declarative YAML, so an agent (or a human) can run "deploy a fresh
mainnet fullnode with snapshot" with one command instead of chaining
five. See AGENTS.md "Workflow" sections for the underlying logic.`,
}

var recipeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available recipes",
	RunE:  runRecipeList,
}

var recipeShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Print one recipe's YAML and parameter list",
	Args:  cobra.RangeArgs(0, 1),
	RunE:  runRecipeShow,
}

var recipeValidateCmd = &cobra.Command{
	Use:   "validate --file <path>",
	Short: "Parse and check a recipe file without running it",
	Long: `Load a recipe from disk, parse it strictly and report every structural
problem at once, without executing a single step.

Running a recipe is a sequence of real operations against real nodes.
The expensive place to find out that a step id is missing, or that
on_failure was misspelled, is step four — after steps one to three have
already changed something. This is the cheap place.`,
	Args: cobra.NoArgs,
	RunE: runRecipeValidate,
}

var (
	recipeRunParams     []string
	recipeRunDryRun     bool
	recipeRunResumeFrom string
	recipeFile          string
	recipeAllowHostExec bool
	recipeShowFile      string
	recipeValidateFile  string
)

// resolveRecipe picks the recipe a command should act on: either a
// built-in by name, or a file via --file. Exactly one, never both — a
// command that silently preferred one would run something other than
// what the caller named.
func resolveRecipe(args []string, file string) (recipe.Recipe, string, error) {
	switch {
	case file != "" && len(args) > 0:
		return recipe.Recipe{}, "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("give a recipe name or --file, not both (got name %q and --file %q)", args[0], file))
	case file != "":
		r, err := recipe.LoadFile(file)
		if err != nil {
			return recipe.Recipe{}, "", output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
		}
		return r, file, nil
	case len(args) > 0:
		r, err := recipe.Get(args[0])
		if err != nil {
			return recipe.Recipe{}, "", output.NewError("NOT_FOUND", output.ExitGeneralError, err.Error())
		}
		return r, "builtin:" + args[0], nil
	default:
		return recipe.Recipe{}, "", output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"specify a recipe name or --file <path>")
	}
}

func runRecipeValidate(cmd *cobra.Command, _ []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	if recipeValidateFile == "" {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, "--file is required")
	}
	r, err := recipe.LoadFile(recipeValidateFile)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}
	res := map[string]any{
		"valid":  true,
		"source": recipeValidateFile,
		"name":   r.Name,
		"steps":  len(r.Steps),
		"params": len(r.Params),
	}
	if outputFmt == "json" {
		return jsonStdout(res)
	}
	fmt.Printf("%s: ok — %d steps, %d params\n", r.Name, len(r.Steps), len(r.Params))
	return nil
}

var recipeRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Execute a recipe end-to-end",
	Long: `Resolve --param key=value inputs, then run the recipe's steps in
order, re-execing trond itself for each step. Step output is
captured as JSON and made available to subsequent steps via
{{ steps.<id>.<field> }} substitution.

Examples:
  trond recipe run nile-test-fullnode \
    --param intent_path=examples/nile-fullnode.yaml

  trond recipe run fresh-mainnet-fullnode-with-snapshot \
    --param intent_path=examples/mainnet-fullnode-snapshot.yaml \
    --param snapshot_dest=/srv/tron/n1

  trond recipe run upgrade-with-verify \
    --param node=my-fullnode \
    --param version=4.8.1 \
    --param intent_path=examples/mainnet-fullnode.yaml`,
	Args: cobra.RangeArgs(0, 1),
	RunE: runRecipeRun,
}

func init() {
	recipeRunCmd.Flags().StringArrayVar(&recipeRunParams, "param", nil,
		"Repeatable key=value param assignments (e.g. --param node=my-node)")
	recipeRunCmd.Flags().BoolVar(&recipeRunDryRun, "dry-run", false,
		"Print each step's resolved command without executing")
	recipeRunCmd.Flags().StringVar(&recipeRunResumeFrom, "resume-from", "",
		"Skip every step before this step ID (use the recipe's `id:` value)")
	recipeRunCmd.Flags().StringVar(&recipeFile, "file", "",
		"Run a recipe from this YAML file instead of a built-in name")
	recipeRunCmd.Flags().BoolVar(&recipeAllowHostExec, "allow-host-exec", false,
		"Permit `kind: host` steps, which run arbitrary programs on this machine")
	recipeShowCmd.Flags().StringVar(&recipeShowFile, "file", "",
		"Show a recipe from this YAML file instead of a built-in name")
	recipeValidateCmd.Flags().StringVar(&recipeValidateFile, "file", "",
		"Recipe YAML file to parse and check")

	recipeCmd.AddCommand(recipeListCmd)
	recipeCmd.AddCommand(recipeShowCmd)
	recipeCmd.AddCommand(recipeRunCmd)
	recipeCmd.AddCommand(recipeValidateCmd)
	rootCmd.AddCommand(recipeCmd)
}

func runRecipeList(cmd *cobra.Command, _ []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	all, err := recipe.LoadEmbedded()
	if err != nil {
		return output.NewError("RECIPE_LOAD_ERROR", output.ExitGeneralError, err.Error())
	}
	rows := make([]map[string]any, 0, len(all))
	for _, r := range all {
		rows = append(rows, map[string]any{
			"name":        r.Name,
			"description": firstParagraph(r.Description),
			"params":      paramSummary(r.Params),
			"step_count":  len(r.Steps),
		})
	}
	if outputFmt == "json" {
		return jsonStdout(map[string]any{"recipes": rows})
	}
	for _, r := range rows {
		fmt.Printf("%-44s  %s\n", r["name"], r["description"])
	}
	return nil
}

func runRecipeShow(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	r, _, err := resolveRecipe(args, recipeShowFile)
	if err != nil {
		return err
	}
	if outputFmt == "json" {
		return jsonStdout(r)
	}
	fmt.Println("Name:", r.Name)
	fmt.Println()
	fmt.Println(strings.TrimSpace(r.Description))
	if len(r.Params) > 0 {
		fmt.Println("\nParams:")
		for _, p := range r.Params {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Printf("  --param %s=<%s>%s\n      %s\n",
				p.Name, defaultStr(p.Type), req, strings.TrimSpace(p.Description))
		}
	}
	fmt.Println("\nSteps:")
	for _, s := range r.Steps {
		fmt.Printf("  %-22s %s %s\n", s.ID, s.Command, strings.Join(s.Args, " "))
	}
	if len(r.Rollback) > 0 {
		fmt.Println("\nRollback:")
		for _, s := range r.Rollback {
			fmt.Printf("  %-22s %s %s\n", s.ID, s.Command, strings.Join(s.Args, " "))
		}
	}
	return nil
}

func runRecipeRun(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	r, source, err := resolveRecipe(args, recipeFile)
	if err != nil {
		return err
	}

	params, err := parseParamFlags(recipeRunParams)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	// Dry-run prints its plan to Out. In json mode that is the same stream
	// the RunResult goes to, so the plan lines land in front of the JSON
	// and nothing downstream can parse it. Send progress to stderr there
	// and leave text mode's stdout behaviour alone.
	planOut := cmd.OutOrStdout()
	if outputFmt == "json" {
		planOut = cmd.ErrOrStderr()
	}

	res, runErr := recipe.Run(cmd.Context(), r, recipe.RunOptions{
		Binary:     exe,
		Params:     params,
		DryRun:     recipeRunDryRun,
		ResumeFrom: recipeRunResumeFrom,
		Out:        planOut,
		Err:        cmd.ErrOrStderr(),
		// Steps are re-execs of this binary and inherit none of our flags.
		// Forward the two that decide WHERE a step acts and WHETHER it is
		// allowed to. paths.BaseDir() is the parent's already-resolved
		// directory, so this covers --state-dir and TROND_STATE_DIR alike.
		StateDir:       paths.BaseDir(),
		RequirePrivate: guard.Requested(),
		AllowHostExec:  recipeAllowHostExec,
	})

	if res != nil {
		res.Source = source
	}
	if outputFmt == "json" && res != nil {
		_ = jsonStdout(res)
	} else if res != nil && !recipeRunDryRun {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nrecipe %s: status=%s, %d steps, %dms\n",
			res.Recipe, res.Status, len(res.Steps), res.DurationMs)
	}
	if runErr != nil {
		// Carry the failing step's exit code rather than flattening every
		// failure to 1. A step refused by the private gate exits 2, and an
		// agent that sees 1 learns "something broke" where the truth was
		// "refused, and here is why".
		exit := output.ExitGeneralError
		if res != nil && res.FailedAt != "" {
			for _, st := range res.Steps {
				if st.ID == res.FailedAt && st.ExitCode > 0 {
					exit = st.ExitCode
				}
			}
		}
		return output.NewError("RECIPE_FAILED", exit, runErr.Error())
	}
	return nil
}

func parseParamFlags(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		idx := strings.IndexByte(p, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("--param expects key=value, got %q", p)
		}
		out[p[:idx]] = p[idx+1:]
	}
	return out, nil
}

func paramSummary(ps []recipe.Param) string {
	if len(ps) == 0 {
		return "(no params)"
	}
	required := 0
	for _, p := range ps {
		if p.Required {
			required++
		}
	}
	return fmt.Sprintf("%d total / %d required", len(ps), required)
}

func defaultStr(s string) string {
	if s == "" {
		return "string"
	}
	return s
}

// firstParagraph re-uses the helper logic from cmd/schema.go but
// internal isolation means we can't import it directly without
// pulling Manifest in. Reimplemented small.
func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
